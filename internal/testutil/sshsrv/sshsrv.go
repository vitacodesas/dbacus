// Package sshsrv es un servidor SSH en proceso que solo soporta direct-tcpip; sirve para probar túneles sin Docker.
package sshsrv

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

const (
	User     = "tunnel"
	Password = "s3cret"
)

type Server struct {
	Addr        string
	Host        string
	Port        int
	HostKey     ssh.PublicKey
	Fingerprint string
	ClientKey   ssh.Signer // clave autorizada
	KeyPath     string     // clave privada del cliente en disco (sin passphrase)

	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	wg    sync.WaitGroup
}

func Start(t *testing.T) *Server {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)

	dir := t.TempDir()
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == User && string(pw) == Password {
				return nil, nil
			}
			return nil, io.ErrUnexpectedEOF
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == User && string(key.Marshal()) == string(clientSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, io.ErrUnexpectedEOF
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp := ln.Addr().(*net.TCPAddr)
	s := &Server{
		Addr: ln.Addr().String(), Host: "127.0.0.1", Port: tcp.Port,
		HostKey: hostSigner.PublicKey(), Fingerprint: ssh.FingerprintSHA256(hostSigner.PublicKey()),
		ClientKey: clientSigner, KeyPath: keyPath, ln: ln,
	}
	s.wg.Add(1)
	go s.acceptLoop(cfg)
	t.Cleanup(s.Close)
	return s
}

func (s *Server) acceptLoop(cfg *ssh.ServerConfig) {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serve(c, cfg)
	}
}

func (s *Server) serve(c net.Conn, cfg *ssh.ServerConfig) {
	defer s.wg.Done()
	defer c.Close()
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go func() {
		for r := range reqs {
			if r.WantReply {
				_ = r.Reply(true, nil) // keepalive@openssh.com
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			_ = nc.Reject(ssh.UnknownChannelType, "solo direct-tcpip")
			continue
		}
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
			_ = nc.Reject(ssh.ConnectionFailed, "payload inválido")
			continue
		}
		dst, err := net.Dial("tcp", net.JoinHostPort(p.Host, itoa(p.Port)))
		if err != nil {
			_ = nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			_ = dst.Close()
			continue
		}
		go ssh.DiscardRequests(chReqs)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer ch.Close()
			defer dst.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(dst, ch); done <- struct{}{} }()
			go func() { _, _ = io.Copy(ch, dst); done <- struct{}{} }()
			<-done
		}()
	}
}

// DropConnections corta todas las conexiones activas (simula caída de red) sin cerrar el listener.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

func (s *Server) Close() {
	_ = s.ln.Close()
	s.DropConnections()
	s.wg.Wait()
}

func itoa(n uint32) string {
	if n == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// EchoServer escucha en loopback y devuelve lo que recibe; sirve como "base de datos" detrás del túnel.
func EchoServer(t *testing.T) (addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	return ln.Addr().String()
}

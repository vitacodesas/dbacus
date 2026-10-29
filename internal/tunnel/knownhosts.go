package tunnel

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/vitacodesas/dbacus/internal/apperr"
)

// KnownHosts guarda huellas SHA256 de host keys SSH confiadas, una por línea: "host:puerto SHA256:…".
// Solo guarda huella (no la clave) porque el flujo de confianza de la UI trabaja con huellas (spec 04).
type KnownHosts struct {
	path string
	mu   sync.Mutex
}

func NewKnownHosts(path string) *KnownHosts { return &KnownHosts{path: path} }

func hostKey(host string, port int) string {
	return net.JoinHostPort(strings.ToLower(strings.Trim(host, "[]")), strconv.Itoa(port))
}

// Check devuelve nil si la clave coincide con la confiada. Desconocida → SSH_HOSTKEY_UNKNOWN (Detail = huella);
// distinta → SSH_HOSTKEY_MISMATCH (bloquea; no hay "ignorar").
func (k *KnownHosts) Check(host string, port int, key ssh.PublicKey) error {
	got := ssh.FingerprintSHA256(key)
	k.mu.Lock()
	known, err := k.load()
	k.mu.Unlock()
	if err != nil {
		return err
	}
	want, ok := known[hostKey(host, port)]
	switch {
	case !ok:
		return apperr.Newf(apperr.SSHHostKeyUnknown, "El servidor SSH %s no es conocido todavía", hostKey(host, port)).WithDetail(got)
	case want != got:
		return apperr.Newf(apperr.SSHHostKeyMismatch,
			"La huella del servidor SSH %s cambió: podría ser un ataque o una reinstalación del servidor", hostKey(host, port)).
			WithDetail(fmt.Sprintf("esperada %s; recibida %s", want, got))
	}
	return nil
}

// Trust registra una huella para un host desconocido. Si ya hay una distinta, falla: hay que usar Forget explícitamente.
func (k *KnownHosts) Trust(host string, port int, fingerprint string) error {
	if !strings.HasPrefix(fingerprint, "SHA256:") || strings.ContainsAny(fingerprint, " \t\r\n") {
		return apperr.New(apperr.Validation, "Huella inválida").WithDetail("fingerprint")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	known, err := k.load()
	if err != nil {
		return err
	}
	if cur, ok := known[hostKey(host, port)]; ok && cur != fingerprint {
		return apperr.New(apperr.SSHHostKeyMismatch, "Ya hay otra huella confiada para este servidor; elimínala antes de confiar en la nueva")
	}
	known[hostKey(host, port)] = fingerprint
	return k.save(known)
}

// Forget elimina la huella de un host (idempotente).
func (k *KnownHosts) Forget(host string, port int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	known, err := k.load()
	if err != nil {
		return err
	}
	delete(known, hostKey(host, port))
	return k.save(known)
}

func (k *KnownHosts) load() (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(k.path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			out[fields[0]] = fields[1]
		}
	}
	return out, sc.Err()
}

func (k *KnownHosts) save(known map[string]string) error {
	var b strings.Builder
	for host, fp := range known {
		fmt.Fprintf(&b, "%s %s\n", host, fp)
	}
	tmp, err := os.CreateTemp(filepath.Dir(k.path), ".known_hosts-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), k.path)
}

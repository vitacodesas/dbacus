package services

import "testing"

func TestHello(t *testing.T) {
	if got := (&PingService{}).Hello("Luis"); got != "Hola, Luis!" {
		t.Fatalf("got %q", got)
	}
}

package sqlsplit

import (
	"strings"
	"testing"
)

// BenchmarkSplit: un script de ~1 MiB con cadenas, comentarios y 10 000 sentencias.
func BenchmarkSplit(b *testing.B) {
	script := strings.Repeat("INSERT INTO t (a, b) VALUES (1, 'x; y'); -- comentario; con punto y coma\n/* bloque; */ SELECT * FROM t WHERE a = \"q;\";\n", 5000)
	b.SetBytes(int64(len(script)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if n := len(Split(script)); n != 10_000 {
			b.Fatalf("sentencias = %d", n)
		}
	}
}

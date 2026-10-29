//go:build integration

// Package mysqlbox levanta MySQL/MariaDB en Docker para tests de integración (spec 11).
package mysqlbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mariadb"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
)

const (
	User     = "root"
	Password = "root"
	Database = "testdb"
)

// Images son las imágenes del matrix por defecto; se pueden sobrescribir con DBACUS_IT_IMAGES="mysql:8.0,mariadb:10.11".
// MySQL 5.7 no tiene imagen arm64: añádela a mano (emulada) si hace falta.
func Images() []string {
	if v := os.Getenv("DBACUS_IT_IMAGES"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"mysql:8.0", "mysql:8.4", "mariadb:10.11"}
}

// dataInMemory monta el directorio de datos en tmpfs: los tests son más rápidos y no dejan volúmenes anónimos
// en el disco de Docker (la imagen declara VOLUME /var/lib/mysql y sin esto cada ejecución crea uno).
func dataInMemory() testcontainers.CustomizeRequestOption {
	return testcontainers.WithTmpfs(map[string]string{"/var/lib/mysql": "rw"})
}

type Instance struct {
	Image string
	Host  string
	Port  int
}

// Start arranca el contenedor y lo termina al acabar el test.
func Start(t *testing.T, image string) Instance {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var (
		ctr testcontainers.Container
		err error
	)
	if strings.HasPrefix(image, "mariadb") {
		ctr, err = mariadb.Run(ctx, image, mariadb.WithUsername(User), mariadb.WithPassword(Password), mariadb.WithDatabase(Database), mariadb.WithDefaultCredentials(), dataInMemory())
	} else {
		ctr, err = mysql.Run(ctx, image, mysql.WithUsername(User), mysql.WithPassword(Password), mysql.WithDatabase(Database), mysql.WithDefaultCredentials(), dataInMemory())
	}
	if err != nil {
		t.Skipf("no se pudo iniciar %s (¿Docker en marcha?): %v", image, err)
	}
	testcontainers.CleanupContainer(t, ctr)

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return Instance{Image: image, Host: host, Port: int(port.Num())}
}

# Changelog

Formato basado en [Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/); versionado semántico.

## [Sin publicar]

## [0.1.0]

Primera versión pública.

### Añadido
- Conexiones MySQL/MariaDB con prueba paso a paso, TLS, túnel SSH, modo solo lectura y contraseñas en el llavero del sistema.
- Explorador de esquema, editor Monaco con pestañas, ejecución cancelable y resultados paginados.
- Edición de celdas, filas nuevas, duplicado y borrado con revisión del SQL y control de conflictos.
- Historial y exportación a CSV y JSON en flujo.

### Cambiado
- El proyecto pasa a llamarse Dbacus (antes «Tabularis Clon»). Al primer arranque se **copian** los perfiles, el historial, `known_hosts` y las contraseñas de la versión anterior; los datos viejos no se borran.

# Política de seguridad

## Versiones con soporte

Solo la última versión publicada recibe correcciones de seguridad.

## Cómo reportar una vulnerabilidad

**No abras un issue público.** Usa «Report a vulnerability» en la pestaña *Security* del repositorio (reporte privado de GitHub). Incluye la versión, el sistema operativo, los pasos para reproducirlo y el impacto que ves.

Responderemos en un plazo razonable y coordinaremos contigo la publicación de la corrección.

## Qué importa especialmente aquí

Dbacus maneja credenciales de bases de datos y ejecuta SQL arbitrario por diseño. Nos interesan sobre todo:

- Fugas de contraseñas o *passphrases* (a disco, logs, la interfaz o el historial).
- Saltarse el modo solo lectura de un perfil.
- Fallos en la verificación de `known_hosts` del túnel SSH o de TLS.
- Inyección de contenido en la interfaz (XSS) a partir de datos de una base de datos.

Ejecutar SQL que tú mismo escribes en una conexión que tú configuraste no es una vulnerabilidad.

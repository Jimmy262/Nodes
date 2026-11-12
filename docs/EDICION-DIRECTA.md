# Editar aplicaciones directamente desde Nodes

En una aplicación, abre **Editar configuración**. Nodes lee Docker por la conexión SSH existente; no pide una clave de Dokploy ni llama a su API para guardar estos cambios.

- **Nombre:** cambia el nombre visible en Nodes, sin renombrar recursos Docker.
- **Variables:** añade o cambia valores del servicio elegido. Los valores que no cambies se conservan y permanecen ocultos por defecto en el formulario.
- **Dominios:** edita rutas simples de un solo `Host` en etiquetas de Traefik o en el archivo convencional del servicio Swarm bajo `/etc/dokploy/traefik/dynamic`. Se conservan puerto, TLS y middlewares al sustituir el host. También puedes añadir una ruta nueva si el servicio ya pertenece a la red del proxy compatible.

Primero pulsa **Revisar cambios**. Nodes valida la definición actual, el dominio y, si corresponde, DNS y HTTPS. **Guardar y aplicar** confirma la escritura. Cambiar variables puede reiniciar tareas o recrear servicios; el nombre visible por sí solo no los reinicia.

## Cómo se guarda

En **Swarm**, Nodes envía la definición revisada a la API Docker a través de SSH y del socket local del servidor. Usa la versión del servicio para rechazar cambios concurrentes, conserva imagen, montajes, redes, secretos/configs de Swarm y opciones ajenas al cambio. Comprueba las tareas actualizadas y guarda un respaldo cifrado en SQLite con la protección de la cuenta del sistema. Las rutas de archivo se sustituyen de forma atómica tras comprobar su contenido anterior y se verifica el archivo final.

Si falla la actualización y la definición todavía coincide con la que envió Nodes, intenta recuperar la anterior. Cuando no puede confirmar el estado del servicio o del archivo, conserva el respaldo y marca que hace falta recuperación; no declara éxito ni sobrescribe cambios ajenos.

En **Compose**, usa el archivo original y las carpetas identificadas por las etiquetas Docker. Las variables modificadas se guardan en archivos privados, fuera del contexto de construcción. Comprueba Compose y actualiza únicamente los servicios afectados, sin reconstruir ni descargar imágenes. Si falla, intenta recuperar la configuración anterior.

## Alcance

- La edición cambia el estado de ejecución y, cuando corresponde, el archivo Compose o la ruta del proxy. No sincroniza la base interna de Dokploy, tu repositorio Git ni otros administradores. Un despliegue desde ellos puede sobrescribir lo editado: administra estos campos desde una sola fuente.
- Los servicios adoptados por un traslado deben estar en estado activo o restaurado. Los estados de recuperación pendientes se mantienen bloqueados.
- Las reglas avanzadas de proxy, backends compartidos/externos y archivos fuera de la ubicación admitida se conservan y no se ofrecen como rutas editables. Añadir un dominio por el proveedor Docker requiere un Swarm de un solo nodo y la red del proxy ya conectada.
- La infraestructura Dokploy/Traefik se protege; solo admite cambiar el nombre visible. Los contenedores sin definición Compose o Swarm conservan esa misma restricción.
- No se borran volúmenes, no se cambia la imagen ni se modifican secretos/configs de Swarm. Una actualización operativa no sustituye un respaldo de los datos de la aplicación.

En Linux conserva `NODO_MASTER_KEY`; en Windows los respaldos cifrados dependen de la identidad DPAPI. Los detalles técnicos y variables nunca deben copiarse a incidencias públicas o capturas sin revisar.

Referencia del comportamiento de actualización y pausas: [actualizaciones de servicios Swarm, Docker](https://docs.docker.com/engine/swarm/swarm-tutorial/rolling-update/).

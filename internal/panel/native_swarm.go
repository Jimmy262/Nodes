package panel

import (
	"context"
	"database/sql"
	"errors"
)

func (a *App) nativeSwarmConfigurationIfPresent(ctx context.Context, s Server, c *applicationConfiguration, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	var binding, stage string
	var protected, encrypted []byte
	err := a.db.QueryRow("SELECT connection_hash,protected_key,definition,stage FROM native_swarm_apps WHERE server_id=? AND service_name=?", s.ID, name).Scan(&binding, &protected, &encrypted, &stage)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if binding != connectionHash(s) {
		return true, errors.New("La conexión del servicio adoptado cambió. Revisa el servidor antes de administrarlo")
	}
	raw, err := openConnectionSecret(s, protected, encrypted)
	if err != nil {
		return true, errors.New("No se pudo abrir la definición protegida de Nodo")
	}
	clear(raw)
	c.Manager = "Swarm · Nodo"
	c.kind = "swarm-native"
	c.appName = name
	c.ConnectionRequired = false
	if stage != "active" && stage != "restored" {
		c.Message = "Nodo conserva la definición de este servicio y su estado de recuperación (" + stage + "). Revisa Actividad antes de volver a trasladarlo."
		return true, nil
	}
	return true, a.loadSwarmConfiguration(ctx, s, c, name)
}

package panel

import (
	"encoding/json"
	"strings"
	"time"
)

// Persist only the reviewed resource identity and human-readable effects.
// Never serialize the plan: it also holds environment values and private files.
type operationDetails struct {
	Name        string   `json:"name,omitempty"`
	Application string   `json:"application,omitempty"`
	ContainerID string   `json:"containerId,omitempty"`
	Image       string   `json:"image,omitempty"`
	Source      string   `json:"source,omitempty"`
	Destination string   `json:"destination,omitempty"`
	Manager     string   `json:"manager,omitempty"`
	URL         string   `json:"url,omitempty"`
	Changes     []string `json:"changes,omitempty"`
}
type operationEvent struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	At      string `json:"at"`
}

func (a *App) recordOperation(p *operationPlan) error {
	d := operationDetails{Name: p.Name, Source: p.Source, Destination: p.Destination, Manager: p.Manager, URL: p.URL, Changes: p.Summary}
	if p.container != nil {
		d.ContainerID = p.container.ID
		d.Image, _ = p.container.Config["Image"].(string)
		a.mu.RLock()
		for _, app := range a.docker[p.server.ID].Applications {
			for _, service := range app.Services {
				if service.ID == d.ContainerID {
					d.Application = app.Name
				}
			}
		}
		a.mu.RUnlock()
		if d.Application == "" {
			for _, label := range []string{"com.docker.compose.project", "com.docker.swarm.service.name"} {
				if project, ok := p.container.labels()[label].(string); ok && project != "" {
					d.Application = friendlyName(project, true)
					break
				}
			}
		}
	}
	if p.edit != nil {
		d.Application = p.edit.before.Name
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	message := "Comprobando la configuración actual…"
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO operations VALUES(?,?,?,'running',?,?)", p.ID, p.Kind, p.server.ID, message, now); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO operation_context(operation_id,details,started_at) VALUES(?,?,?)", p.ID, string(raw), now); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO operation_events(operation_id,status,message,at) VALUES(?,'running',?,?)", p.ID, message, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) operationEvents(items []map[string]any) error {
	if len(items) == 0 {
		return nil
	}
	indexes := map[string]int{}
	args := []any{}
	placeholders := []string{}
	for i, item := range items {
		id := item["id"].(string)
		indexes[id] = i
		args = append(args, id)
		placeholders = append(placeholders, "?")
	}
	rows, err := a.db.Query("SELECT operation_id,status,message,at FROM operation_events WHERE operation_id IN ("+strings.Join(placeholders, ",")+") ORDER BY id", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var event operationEvent
		if err = rows.Scan(&id, &event.Status, &event.Message, &event.At); err != nil {
			return err
		}
		i := indexes[id]
		items[i]["events"] = append(items[i]["events"].([]operationEvent), event)
	}
	return rows.Err()
}

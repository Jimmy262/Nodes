package panel

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"runtime"
)

// Keep schema initialization and storage policy separate from HTTP routing.
func openDatabase(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Windows uses account ACLs and DPAPI; on Unix keep the database and its
	// subsequently created WAL files private to the service account.
	if runtime.GOOS != "windows" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		err = file.Chmod(0600)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS servers(id INTEGER PRIMARY KEY,name TEXT NOT NULL,host TEXT NOT NULL,port INTEGER NOT NULL,user TEXT NOT NULL,identity TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS samples(server_id INTEGER NOT NULL,at TEXT NOT NULL,cpu REAL,memory REAL,disk REAL,uptime REAL,rx REAL,tx REAL);
 CREATE INDEX IF NOT EXISTS samples_lookup ON samples(server_id,at);
 CREATE INDEX IF NOT EXISTS samples_retention ON samples(at);
 CREATE TABLE IF NOT EXISTS auth_credentials(id INTEGER PRIMARY KEY CHECK(id=1),password_hash TEXT NOT NULL,updated_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS auth_sessions(token_hash TEXT PRIMARY KEY,expires_at INTEGER NOT NULL,remember INTEGER NOT NULL,created_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS auth_throttle(id INTEGER PRIMARY KEY CHECK(id=1),failures INTEGER NOT NULL,window_start INTEGER NOT NULL,blocked_until INTEGER NOT NULL);
 INSERT OR IGNORE INTO auth_throttle VALUES(1,0,0,0);
 CREATE TABLE IF NOT EXISTS operations(id TEXT PRIMARY KEY,kind TEXT NOT NULL,server_id INTEGER NOT NULL,status TEXT NOT NULL,message TEXT NOT NULL,updated_at TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS operations_recent ON operations(updated_at DESC);
 CREATE TABLE IF NOT EXISTS operation_context(operation_id TEXT PRIMARY KEY,details TEXT NOT NULL,started_at TEXT NOT NULL,finished_at TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS operation_events(id INTEGER PRIMARY KEY,operation_id TEXT NOT NULL,status TEXT NOT NULL,message TEXT NOT NULL,at TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS operation_events_lookup ON operation_events(operation_id,id);
 CREATE TABLE IF NOT EXISTS application_names(app_id TEXT PRIMARY KEY,server_id INTEGER NOT NULL,name TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS dokploy_connections(server_id INTEGER PRIMARY KEY,connection_hash TEXT NOT NULL,protected_key BLOB NOT NULL,secret BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS native_swarm_apps(server_id INTEGER NOT NULL,service_name TEXT NOT NULL,operation_id TEXT NOT NULL,connection_hash TEXT NOT NULL,protected_key BLOB NOT NULL,definition BLOB NOT NULL,stage TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(server_id,service_name));
 CREATE TABLE IF NOT EXISTS github_connection(id INTEGER PRIMARY KEY CHECK(id=1),login TEXT NOT NULL,protected_key BLOB NOT NULL,secret BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS github_deployments(id TEXT PRIMARY KEY,server_id INTEGER NOT NULL,connection_hash TEXT NOT NULL,details TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS terminal_sessions(id TEXT PRIMARY KEY,server_id INTEGER NOT NULL,kind TEXT NOT NULL,connection_hash TEXT NOT NULL,created_at INTEGER NOT NULL,UNIQUE(server_id,kind));`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

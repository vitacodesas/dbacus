// Package model contiene los DTOs neutrales compartidos por store, driver y services (spec 02/03).
// Etiquetas json en camelCase: son el contrato con Vue.
package model

import "time"

type TLSConfig struct {
	Mode     string `json:"mode"` // disable|preferred|required|verify-ca|verify-full
	CAPath   string `json:"caPath"`
	CertPath string `json:"certPath"`
	KeyPath  string `json:"keyPath"`
}

// SSHConfig no lleva secretos: password/passphrase viajan en ConnectionSecrets y viven en el llavero.
type SSHConfig struct {
	Enabled    bool   `json:"enabled"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	AuthMethod string `json:"authMethod"` // password|key
	KeyPath    string `json:"keyPath"`
}

type ConnectionProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Database string `json:"database"`
	Color    string `json:"color"`
	// GroupID es el grupo al que pertenece (spec 17); vacío = «Sin grupo». Va en el JSON del perfil, sin columna propia.
	GroupID      string            `json:"groupId"`
	TLS          TLSConfig         `json:"tls"`
	SSH          SSHConfig         `json:"ssh"`
	Params       map[string]string `json:"params"`
	SavePassword bool              `json:"savePassword"`
	// NoHistory desactiva el historial de consultas de este perfil (spec 07). Cero = se guarda.
	NoHistory bool `json:"noHistory"`
	// ReadOnly abre las conexiones con SET SESSION TRANSACTION READ ONLY y rechaza en Go lo que no sea lectura (spec 10).
	ReadOnly bool `json:"readOnly"`
	// Estado de los secretos en el llavero; los fija el servicio (el cliente no puede escribirlos).
	HasPassword      bool      `json:"hasPassword"`
	HasSSHPassword   bool      `json:"hasSshPassword"`
	HasSSHPassphrase bool      `json:"hasSshPassphrase"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ConnectionGroup agrupa conexiones y les da una configuración común (spec 17). Hoy solo color.
type ConnectionGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type HistoryEntry struct {
	ID           string    `json:"id"`
	ProfileID    string    `json:"profileId"`
	SQL          string    `json:"sql"`
	Database     string    `json:"database"`
	ExecutedAt   time.Time `json:"executedAt"`
	ElapsedMs    int64     `json:"elapsedMs"`
	OK           bool      `json:"ok"`
	RowCount     int64     `json:"rowCount"`
	AffectedRows int64     `json:"affectedRows"`
	ErrorMessage string    `json:"errorMessage"`
}

type Settings struct {
	Theme               string  `json:"theme"` // light|dark|system
	Locale              string  `json:"locale"`
	MaxRows             int     `json:"maxRows"`
	MaxResultMemoryMiB  int     `json:"maxResultMemoryMiB"`
	HistoryRetention    int     `json:"historyRetention"`
	ShowSystemDatabases bool    `json:"showSystemDatabases"`
	ConfirmEdits        bool    `json:"confirmEdits"` // pedir confirmación (con el SQL a la vista) antes de aplicar ediciones de celdas
	SidebarWidth        int     `json:"sidebarWidth"`
	SplitRatio          float64 `json:"splitRatio"`
	// CollapsedGroups son los ids de los grupos de conexiones plegados en la lista (spec 17); "__none__" es «Sin grupo».
	CollapsedGroups []string `json:"collapsedGroups"`
}

func DefaultSettings() Settings {
	return Settings{
		Theme:              "system",
		Locale:             "es",
		MaxRows:            100_000,
		MaxResultMemoryMiB: 512,
		HistoryRetention:   5000,
		ConfirmEdits:       true,
		SidebarWidth:       300,
		SplitRatio:         0.5,
		CollapsedGroups:    []string{},
	}
}

type TabState struct {
	ID               string `json:"id"`
	SessionProfileID string `json:"sessionProfileId"`
	Title            string `json:"title"`
	SQL              string `json:"sql"`
	Database         string `json:"database"`
	Order            int    `json:"order"`
}

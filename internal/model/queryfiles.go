package model

import "time"

// DTOs de las consultas guardadas (spec 20). Las rutas son siempre relativas a la raíz y usan "/".

type QueryRoot struct {
	Path      string `json:"path"`
	IsDefault bool   `json:"isDefault"`
	Exists    bool   `json:"exists"`
	Writable  bool   `json:"writable"`
}

const (
	QueryNodeFolder = "folder"
	QueryNodeFile   = "file"
)

type QueryNode struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"` // sin ".sql"
	Kind        string    `json:"kind"`
	Size        int64     `json:"size"`
	ModifiedAt  time.Time `json:"modifiedAt"`
	Version     string    `json:"version"` // solo archivos
	ProfileID   string    `json:"profileId"`
	ProfileName string    `json:"profileName"`
	Database    string    `json:"database"`
}

type QueryTree struct {
	Root      QueryRoot   `json:"root"`
	Nodes     []QueryNode `json:"nodes"`
	Truncated bool        `json:"truncated"`
}

type QueryFile struct {
	Path           string `json:"path"`
	Name           string `json:"name"`
	SQL            string `json:"sql"`
	Version        string `json:"version"`
	ProfileID      string `json:"profileId"`
	ProfileName    string `json:"profileName"`
	Database       string `json:"database"`
	ProfileMissing bool   `json:"profileMissing"`
}

type SaveQueryRequest struct {
	Path               string `json:"path"`
	SQL                string `json:"sql"`
	ProfileID          string `json:"profileId"`
	Database           string `json:"database"`
	RememberConnection bool   `json:"rememberConnection"`
	// ExpectedVersion vacío = archivo nuevo (falla si existe); con valor, actualiza y compara con el disco.
	ExpectedVersion string `json:"expectedVersion"`
	// Overwrite ignora tanto la existencia como la versión.
	Overwrite bool `json:"overwrite"`
}

type QueryRef struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type PathChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type QuerySearchRequest struct {
	Text    string `json:"text"`
	Content bool   `json:"content"`
	Limit   int    `json:"limit"`
}

type QueryHit struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Line    int    `json:"line"` // 0 si la coincidencia es solo por nombre
	Snippet string `json:"snippet"`
}

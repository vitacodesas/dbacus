package model

import "time"

// MonitorRun es una sesión de monitoreo (spec 22 ⚑D15) tal como la ve Vue. El ID es texto: en el store es numérico.
type MonitorRun struct {
	ID            string     `json:"id"`
	ProfileID     string     `json:"profileId"`
	StartedAt     time.Time  `json:"startedAt"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	Reason        string     `json:"reason"` // running | stopped | disconnected | app_closed | crashed
	IntervalSec   int        `json:"intervalSec"`
	ServerVersion string     `json:"serverVersion"`
	ServerID      string     `json:"serverId"`
	Samples       int        `json:"samples"`
	Gaps          int        `json:"gaps"`
}

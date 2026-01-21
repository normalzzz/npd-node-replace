package config

import (
	"encoding/json"
	"os"
	"io"
	"errors"

	log "github.com/sirupsen/logrus"
)

type Action string

const (
	ActionReboot  Action = "reboot"
	ActionReplace Action = "replace"
)

// type Tolerance struct{
// 	Tolerate map[ReasonAndTime]Action `json:"tolerate"`
// }

type Tolerance struct {
	Times int32 `json:"times"`

	Action Action `json:"action"`

	TimeWindowInMinutes int32 `json:"timewindowinminutes"`
}

type ToleranceCollection struct {
	ToleranceCollection map[string]Tolerance `json:"tolerancecollection"`
}

// we should load configuration file first, otherwise exit directly
func LoadConfiguration() (*ToleranceCollection, error) {
	filed, err := os.Open("tolerance.json")

	if err != nil {
		if os.IsNotExist(err) {
			log.Warn("tolerance.json not found, skip loading config")
			return nil, nil
		}
		return nil, err
	}

	defer filed.Close()


	stat, err := filed.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() == 0 {
		log.Warn("tolerance.json is empty")
		return nil, errors.New("tolerance.json is empty")
	}

	var toleranceColl ToleranceCollection
	decoder := json.NewDecoder(filed)

	if err := decoder.Decode(&toleranceColl); err != nil {
		if err == io.EOF {
			log.Warn("tolerance.json is empty or whitespace")
			return nil, err
		}
		log.Error("invalid tolerance.json format, ignore config", err)
		return nil, err
	}

	return &toleranceColl, nil
}

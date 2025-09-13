package config

import (
	"encoding/json"
	"os"

	log "github.com/sirupsen/logrus"
)

type Action string

const (
	ActionBoot    Action = "reboot"
	ActionReplace Action = "replace"
)

// type Tolerance struct{
// 	Tolerate map[ReasonAndTime]Action `json:"tolerate"`
// }

type Tolerance struct {
	Times int `json:"times"`

	Action Action `json:"action"`
}

type ToleranceCollection struct {
	ToleranceCollection map[string]Tolerance `json:"tolerancecollection"`
}

// we should load configuration file first, otherwise exit directly
func LoadConfiguration() (ToleranceCollection, error) {
	filed, err := os.Open("tolerance.json")

	if err != nil {
		log.Error("can not open configuration file tolerance.json", err)
	}

	defer filed.Close()

	var toleranceColl ToleranceCollection

	if err := json.NewDecoder(filed).Decode(&toleranceColl); err != nil {
		log.Error("faild to decode tolerance config file, please check config file", err)
	}

	return toleranceColl, err

}

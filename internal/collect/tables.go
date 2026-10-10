package collect

import (
	"encoding/json"
	"fmt"
)

// tableTypes maps table names to decoders, so tables sent over the network
// as JSON come back as the same Go types the widgets expect.
var tableTypes = map[string]func(json.RawMessage) (any, error){
	TableCPUInfo:     decodeAs[CPUInfo],
	TableSystem:      decodeAs[SystemInfo],
	TableProcesses:   decodeAs[[]Process],
	TableFilesystems: decodeAs[[]Filesystem],
	TableDisks:       decodeAs[[]DiskIO],
	TableInterfaces:  decodeAs[[]NetInterface],
	TableSensors:     decodeAs[[]Sensor],
	TableGPUs:        decodeAs[[]GPU],
	TableGPUProcs:    decodeAs[[]GPUProcess],
	TableBatteries:   decodeAs[[]Battery],
	TableContainers:  decodeAs[[]Container],
}

func decodeAs[T any](raw json.RawMessage) (any, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}

// DecodeTable decodes a JSON table by its name.
func DecodeTable(name string, raw json.RawMessage) (any, error) {
	dec, ok := tableTypes[name]
	if !ok {
		return nil, fmt.Errorf("unknown table %q", name)
	}
	return dec(raw)
}

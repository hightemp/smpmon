package collect

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestTableRoundTrip(t *testing.T) {
	procs := []Process{{PID: 1, Name: "init", State: 'S', StartTime: time.Unix(100, 0).UTC(), CPUTime: time.Second}}
	raw, err := json.Marshal(procs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTable(TableProcesses, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, procs) {
		t.Fatalf("got %+v", got)
	}
	if _, err := DecodeTable("nope", raw); err == nil {
		t.Fatal("unknown table accepted")
	}
	// Every table constant has a decoder.
	for _, name := range []string{TableCPUInfo, TableSystem, TableProcesses, TableFilesystems, TableDisks,
		TableInterfaces, TableSensors, TableGPUs, TableGPUProcs, TableBatteries, TableContainers} {
		if _, ok := tableTypes[name]; !ok {
			t.Errorf("no decoder for %s", name)
		}
	}
}

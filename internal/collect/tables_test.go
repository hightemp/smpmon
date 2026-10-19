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

func TestGPUTablesFromOlderAgents(t *testing.T) {
	// A 2.0 agent sends GPUs and processes without the newer fields.
	got, err := DecodeTable(TableGPUs, json.RawMessage(`[{"Index":0,"Name":"RTX","Util":5,"Fan":-1}]`))
	if err != nil {
		t.Fatal(err)
	}
	g := got.([]GPU)[0]
	if g.Util != 5 || g.Enc != -1 || g.Dec != -1 || g.PCIeRX != -1 || g.PState != -1 || g.Throttle != nil {
		t.Fatalf("gpu %+v", g)
	}
	got, err = DecodeTable(TableGPUProcs, json.RawMessage(`[{"PID":7,"GPU":0,"MemUsed":1024}]`))
	if err != nil {
		t.Fatal(err)
	}
	if p := got.([]GPUProcess)[0]; p.PID != 7 || p.MemUsed != 1024 || p.Util != -1 || p.Enc != -1 {
		t.Fatalf("proc %+v", p)
	}
	// Current values survive a round trip, including "no throttling".
	in := UnknownGPU()
	in.Enc, in.PState, in.Throttle = 3, 2, []string{}
	raw, _ := json.Marshal([]GPU{in})
	got, _ = DecodeTable(TableGPUs, raw)
	if out := got.([]GPU)[0]; !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip %+v", out)
	}
}

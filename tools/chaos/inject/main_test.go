package main

import (
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
)

func TestDirectPartitionClientConfigurationIsValid(t *testing.T) {
	client, err := newClient()
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
}

func TestPoisonIsRejectedByRealEnvelopeParser(t *testing.T) {
	r := poison("marker", "ChaosPoison")
	if _, err := messaging.FromRecord(r); err == nil {
		t.Fatal("poison unexpectedly has a valid envelope")
	}
}

func TestDLTEvidenceMustMatchRecordAndHeaders(t *testing.T) {
	good := func() *kgo.Record {
		r := poison("marker", "ChaosPoison")
		r.Topic += contracts.DLTSuffix
		r.Headers = append(r.Headers,
			kgo.RecordHeader{Key: messaging.HeaderDLTTopic, Value: []byte(contracts.TopicLogisticsCommands)},
			kgo.RecordHeader{Key: messaging.HeaderDLTError, Value: []byte("missing message-id")})
		return r
	}
	if !matches(good(), "marker", "ChaosPoison") {
		t.Fatal("valid DLT evidence rejected")
	}
	for _, mutate := range []func(*kgo.Record){
		func(r *kgo.Record) { r.Key = []byte("unrelated") },
		func(r *kgo.Record) { r.Topic = contracts.TopicLogisticsCommands },
		func(r *kgo.Record) { r.Headers = r.Headers[:1] },
		func(r *kgo.Record) { r.Headers[0].Value = []byte("unrelated") },
		func(r *kgo.Record) { r.Headers[2].Value = nil },
	} {
		r := good()
		mutate(r)
		if matches(r, "marker", "ChaosPoison") {
			t.Fatal("unrelated or incomplete DLT record accepted")
		}
	}
}

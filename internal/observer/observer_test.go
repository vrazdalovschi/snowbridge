// PROPRIETARY AND CONFIDENTIAL
//
// Unauthorized copying of this file via any medium is strictly prohibited.
//
// Copyright (c) 2020-2021 Snowplow Analytics Ltd. All rights reserved.

package observer

import (
	"github.com/stretchr/testify/assert"
	"testing"
	"time"

	"github.com/snowplow-devops/stream-replicator/internal/models"
)

// --- Test StatsReceiver

type TestStatsReceiver struct {
	onSend func(b *models.ObserverBuffer)
}

func (s *TestStatsReceiver) Send(b *models.ObserverBuffer) {
	s.onSend(b)
}

// --- Tests

func TestObserver_TestStatsReceiver(t *testing.T) {
	assert := assert.New(t)

	counter := 0
	onSend := func(b *models.ObserverBuffer) {
		assert.NotNil(b)
		if counter == 0 {
			assert.Equal(int64(5), b.TargetResults)
			counter++
		} else {
			assert.Equal(int64(1), b.TargetResults)
		}
	}

	sr := TestStatsReceiver{onSend: onSend}

	observer := New(&sr, 1*time.Second, 3*time.Second)
	assert.NotNil(observer)
	observer.Start()

	// This does nothing
	observer.Start()

	// Push some results
	timeNow := time.Now().UTC()
	messages := []*models.Message{
		{
			Data:         []byte("Baz"),
			PartitionKey: "partition1",
			TimeCreated:  timeNow.Add(time.Duration(-50) * time.Minute),
			TimePulled:   timeNow.Add(time.Duration(-4) * time.Minute),
		},
		{
			Data:         []byte("Bar"),
			PartitionKey: "partition2",
			TimeCreated:  timeNow.Add(time.Duration(-70) * time.Minute),
			TimePulled:   timeNow.Add(time.Duration(-7) * time.Minute),
		},
		{
			Data:         []byte("Foo"),
			PartitionKey: "partition3",
			TimeCreated:  timeNow.Add(time.Duration(-30) * time.Minute),
			TimePulled:   timeNow.Add(time.Duration(-10) * time.Minute),
		},
	}
	r := models.NewWriteResultWithTime(2, 1, timeNow, messages)
	for i := 0; i < 5; i++ {
		observer.TargetWrite(r)
	}

	// Trigger timeout (1 second)
	time.Sleep(2 * time.Second)

	// Trigger flush (3 seconds) - first counter check
	time.Sleep(3 * time.Second)

	// Trigger emergency flush (4 seconds) - second counter check
	observer.TargetWrite(r)
	observer.Stop()
}

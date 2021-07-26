// PROPRIETARY AND CONFIDENTIAL
//
// Unauthorized copying of this file via any medium is strictly prohibited.
//
// Copyright (c) 2020-2021 Snowplow Analytics Ltd. All rights reserved.

package target

import (
	"context"
	"fmt"
	"os"
	"time"

	eventhub "github.com/Azure/azure-event-hubs-go/v3"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/snowplow-devops/stream-replicator/pkg/models"
)

// EventHubConfig holds a config object for Azure EventHub
type EventHubConfig struct {
	EventHubNamespace       string
	EventHubName            string
	MaxAutoRetries          int
	MessageByteLimit        int
	ChunkByteLimit          int
	ChunkMessageLimit       int
	ContextTimeoutInSeconds int
	BatchByteLimit          int
}

// EventHubTarget holds a new client for writing messages to Azure EventHub
type EventHubTarget struct {
	client                  *eventhub.Hub
	eventHubNamespace       string
	eventHubName            string
	messageByteLimit        int
	chunkByteLimit          int
	chunkMessageLimit       int
	contextTimeoutInSeconds int
	batchByteLimit          int

	log *log.Entry
}

// NewEventHubTarget creates a new client for writing messages to Azure EventHub
func NewEventHubTarget(cfg *EventHubConfig) (*EventHubTarget, error) {

	_, keyNamePresent := os.LookupEnv("EVENTHUB_KEY_NAME")
	_, keyValuePresent := os.LookupEnv("EVENTHUB_KEY_VALUE")

	_, connStringPresent := os.LookupEnv("EVENTHUB_CONNECTION_STRING")

	_, tenantIDPresent := os.LookupEnv("AZURE_TENANT_ID")
	_, clientIDPresent := os.LookupEnv("AZURE_CLIENT_ID")

	_, clientSecretPresent := os.LookupEnv("AZURE_CLIENT_SECRET")

	_, azCertPathPresent := os.LookupEnv("AZURE_CERTIFICATE_PATH")
	_, azCertPwrdPresent := os.LookupEnv("AZURE_CERTIFICATE_PASSWORD")

	if !(connStringPresent || (keyNamePresent && keyValuePresent) || (tenantIDPresent && clientIDPresent && ((azCertPathPresent && azCertPwrdPresent) || clientSecretPresent))) {
		return nil, errors.Errorf("Error initialising EventHub client: No valid combination of authentication Env vars found. https://pkg.go.dev/github.com/Azure/azure-event-hubs-go#NewHubWithNamespaceNameAndEnvironment")
	}

	hub, err := eventhub.NewHubWithNamespaceNameAndEnvironment(cfg.EventHubNamespace, cfg.EventHubName, eventhub.HubWithSenderMaxRetryCount(cfg.MaxAutoRetries))
	// Using HubWithSenderMaxRetryCount limits the amount of retries that are handled by the eventhubs package natively (this app handles retries externally to this also)
	// If none is specified, it will retry indefinitely until the context times out, which hides the actual error message
	// To avoid obscuring errors, contextTimeoutInSeconds should be configured to ensure all retries may be completed before its expiry

	return &EventHubTarget{
		client:                  hub,
		eventHubNamespace:       cfg.EventHubNamespace,
		eventHubName:            cfg.EventHubName,
		messageByteLimit:        cfg.MessageByteLimit,
		chunkByteLimit:          cfg.ChunkByteLimit,
		chunkMessageLimit:       cfg.ChunkMessageLimit,
		contextTimeoutInSeconds: cfg.ContextTimeoutInSeconds,
		batchByteLimit:          cfg.BatchByteLimit,

		log: log.WithFields(log.Fields{"target": "eventhub", "cloud": "Azure", "namespace": cfg.EventHubNamespace, "eventhub": cfg.EventHubName}),
	}, err
}

func (eht *EventHubTarget) Write(messages []*models.Message) (*models.TargetWriteResult, error) {
	eht.log.Debugf("Writing %d messages to stream ...", len(messages))

	chunks, oversized := models.GetChunkedMessages(
		messages,
		eht.chunkMessageLimit,                // Max Chunk size (number of messages)
		eht.MaximumAllowedMessageSizeBytes(), // Message byte limit
		eht.chunkByteLimit,                   // Chunk byte limit
	)

	writeResult := &models.TargetWriteResult{
		Oversized: oversized,
	}

	var errResult error

	for _, chunk := range chunks {
		res, err := eht.process(chunk)
		writeResult = writeResult.Append(res)

		if err != nil {
			errResult = multierror.Append(errResult, err)
		}
	}

	if errResult != nil {
		errResult = errors.Wrap(errResult, "Error writing messages to EventHub")
	}

	eht.log.Debugf("Successfully wrote %d/%d messages", writeResult.SentCount, writeResult.Total())
	return writeResult, errResult
}

func (eht *EventHubTarget) process(messages []*models.Message) (*models.TargetWriteResult, error) {
	messageCount := len(messages)
	eht.log.Debugf("Writing chunk of %d messages to eventHub ...", messageCount)

	ehBatch := make([]*eventhub.Event, messageCount)
	for i, msg := range messages {
		ehEvent := eventhub.NewEvent(msg.Data)
		ehEvent.PartitionKey = &msg.PartitionKey
		ehBatch[i] = ehEvent
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(eht.contextTimeoutInSeconds)*time.Second)
	defer cancel()

	batchIterator := eventhub.NewEventBatchIterator(ehBatch...)
	err := eht.client.SendBatch(ctx, batchIterator, eventhub.BatchWithMaxSizeInBytes(eht.batchByteLimit))

	if err != nil {
		// If we hit an error, we can't distinguish successful batches from the failed one(s), so we return the whole chunk as failed
		return models.NewTargetWriteResult(
			nil,
			messages,
			nil,
			nil,
		), errors.Wrap(err, "Failed to send message batch to EventHub")
	}

	// If no error, all messages were successes
	for _, msg := range messages {
		if msg.AckFunc != nil {
			msg.AckFunc()
		}
	}

	eht.log.Debugf("Successfully wrote chunk of %d messages", len(messages))
	return models.NewTargetWriteResult(
		messages,
		nil,
		nil,
		nil,
	), nil
}

// Open does not do anything for this target
func (eht *EventHubTarget) Open() {}

// Close closes the eventhub client.
func (eht *EventHubTarget) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(10)*time.Second)
	defer cancel()
	eht.client.Close(ctx)
}

// MaximumAllowedMessageSizeBytes returns the max number of bytes that can be sent
// per message for this target
func (eht *EventHubTarget) MaximumAllowedMessageSizeBytes() int {
	return eht.messageByteLimit
}

// GetID returns an identifier for this target
func (eht *EventHubTarget) GetID() string {
	return fmt.Sprintf("sb://%s.servicebus.windows.net/;EntityPath=%s", eht.eventHubNamespace, eht.eventHubName)
}
// PROPRIETARY AND CONFIDENTIAL
//
// Unauthorized copying of this file via any medium is strictly prohibited.
//
// Copyright (c) 2020-2021 Snowplow Analytics Ltd. All rights reserved.

package transform

import (
	"testing"

	"github.com/snowplow-devops/stream-replicator/pkg/models"
	"github.com/stretchr/testify/assert"
)

func TestNewSpEnrichedFilterFunction(t *testing.T) {
	assert := assert.New(t)

	var messageGood = models.Message{
		Data:         snowplowTsv3,
		PartitionKey: "some-key",
	}

	// Single value cases
	aidFilterFuncKeep, _ := NewSpEnrichedFilterFunction("app_id==test-data3")

	// TODO: sort out numbering for fail cases...
	aidKeepIn, aidKeepOut, fail, _ := aidFilterFuncKeep(&messageGood, nil)

	assert.Equal(snowplowTsv3, aidKeepIn.Data)
	assert.Nil(aidKeepOut)
	assert.Nil(fail)

	aidFilterFuncDiscard, _ := NewSpEnrichedFilterFunction("app_id==failThis")

	aidDiscardIn, aidDiscardOut, fail2, _ := aidFilterFuncDiscard(&messageGood, nil)

	assert.Nil(aidDiscardIn)
	assert.Equal(snowplowTsv3, aidDiscardOut.Data)
	assert.Nil(fail2)

	// int value
	urlPrtFilterFuncKeep, _ := NewSpEnrichedFilterFunction("page_urlport==80")

	urlPrtKeepIn, urlPrtKeepOut, fail, _ := urlPrtFilterFuncKeep(&messageGood, nil)

	assert.Equal(snowplowTsv3, urlPrtKeepIn.Data)
	assert.Nil(urlPrtKeepOut)
	assert.Nil(fail)

	// Multiple value cases
	aidFilterFuncKeepWithMultiple, _ := NewSpEnrichedFilterFunction("app_id==someotherValue|test-data3")

	aidMultipleNegationFailedIn, aidMultipleKeepOut, fail3, _ := aidFilterFuncKeepWithMultiple(&messageGood, nil)

	assert.Equal(snowplowTsv3, aidMultipleNegationFailedIn.Data)
	assert.Nil(aidMultipleKeepOut)
	assert.Nil(fail3)

	aidFilterFuncDiscardWithMultiple, _ := NewSpEnrichedFilterFunction("app_id==someotherValue|failThis")

	aidNegationMultipleIn, aidMultipleDiscardOut, fail3, _ := aidFilterFuncDiscardWithMultiple(&messageGood, nil)

	assert.Nil(aidNegationMultipleIn)
	assert.Equal(snowplowTsv3, aidMultipleDiscardOut.Data)
	assert.Nil(fail3)

	// Single value negation cases

	aidFilterFuncNegationDiscard, _ := NewSpEnrichedFilterFunction("app_id!=test-data3")

	aidNegationIn, aidNegationOut, fail4, _ := aidFilterFuncNegationDiscard(&messageGood, nil)

	assert.Nil(aidNegationIn)
	assert.Equal(snowplowTsv3, aidNegationOut.Data)
	assert.Nil(fail4)

	aidFilterFuncNegationKeep, _ := NewSpEnrichedFilterFunction("app_id!=failThis")

	aidNegationFailedIn, aidNegationFailedOut, fail5, _ := aidFilterFuncNegationKeep(&messageGood, nil)

	assert.Equal(snowplowTsv3, aidNegationFailedIn.Data)
	assert.Nil(aidNegationFailedOut)
	assert.Nil(fail5)

	// Multiple value negation cases
	aidFilterFuncNegationDiscardMultiple, _ := NewSpEnrichedFilterFunction("app_id!=someotherValue|test-data1|test-data2|test-data3")

	aidNegationMultipleIn, aidNegationMultipleOut, fail6, _ := aidFilterFuncNegationDiscardMultiple(&messageGood, nil)

	assert.Nil(aidNegationMultipleIn)
	assert.Equal(snowplowTsv3, aidNegationMultipleOut.Data)
	assert.Nil(fail6)

	aidFilterFuncNegationKeptMultiple, _ := NewSpEnrichedFilterFunction("app_id!=someotherValue|failThis")

	aidMultipleNegationFailedIn, aidMultipleNegationFailedOut, fail7, _ := aidFilterFuncNegationKeptMultiple(&messageGood, nil)

	assert.Equal(snowplowTsv3, aidMultipleNegationFailedIn.Data)
	assert.Nil(aidMultipleNegationFailedOut)
	assert.Nil(fail7)

	// Filters on a nil field
	txnFilterFunctionAffirmation, _ := NewSpEnrichedFilterFunction("txn_id==something")

	nilAffirmationIn, nilAffirmationOut, fail8, _ := txnFilterFunctionAffirmation(&messageGood, nil)

	assert.Nil(nilAffirmationIn)
	assert.Equal(snowplowTsv3, nilAffirmationOut.Data)
	assert.Nil(fail8)

	txnFilterFunctionNegation, _ := NewSpEnrichedFilterFunction("txn_id!=something")

	nilNegationIn, nilNegationOut, fail8, _ := txnFilterFunctionNegation(&messageGood, nil)

	assert.Equal(snowplowTsv3, nilNegationIn.Data)
	assert.Nil(nilNegationOut)
	assert.Nil(fail8)
}

func TestNewSpEnrichedFilterFunction_Error(t *testing.T) {
	assert := assert.New(t)
	error := `Invalid filter function config, must be of the format {field name}=={value}[|{value}|...] or {field name}!={value}[|{value}|...]`

	filterFunc, err1 := NewSpEnrichedFilterFunction("")

	assert.Nil(filterFunc)
	assert.Equal(error, err1.Error())

	filterFunc, err2 := NewSpEnrichedFilterFunction("app_id==abc|")

	assert.Nil(filterFunc)
	assert.Equal(error, err2.Error())

	filterFunc, err3 := NewSpEnrichedFilterFunction("!=abc")

	assert.Nil(filterFunc)
	assert.Equal(error, err3.Error())
}

func TestSpEnrichedFilterFunction_Slice(t *testing.T) {
	assert := assert.New(t)

	var filter1Kept = []*models.Message{
		{
			Data:         snowplowTsv1,
			PartitionKey: "some-key",
		},
	}

	var filter1Discarded = []*models.Message{
		{
			Data:         snowplowTsv2,
			PartitionKey: "some-key1",
		},
		{
			Data:         snowplowTsv3,
			PartitionKey: "some-key2",
		},
	}

	filterFunc, _ := NewSpEnrichedFilterFunction("app_id==test-data1")

	filter1 := NewTransformation(filterFunc)
	filter1Res := filter1(messages)

	assert.Equal(len(filter1Kept), len(filter1Res.Result))
	assert.Equal(len(filter1Discarded), len(filter1Res.Filtered))
	assert.Equal(1, len(filter1Res.Invalid))

	var filter2Kept = []*models.Message{
		{
			Data:         snowplowTsv1,
			PartitionKey: "some-key",
		},
		{
			Data:         snowplowTsv2,
			PartitionKey: "some-key1",
		},
	}

	var filter2Discarded = []*models.Message{

		{
			Data:         snowplowTsv3,
			PartitionKey: "some-key2",
		},
	}

	filterFunc2, _ := NewSpEnrichedFilterFunction("app_id==test-data1|test-data2")

	filter2 := NewTransformation(filterFunc2)
	filter2Res := filter2(messages)

	assert.Equal(len(filter2Kept), len(filter2Res.Result))
	assert.Equal(len(filter2Discarded), len(filter2Res.Filtered))
	assert.Equal(1, len(filter2Res.Invalid))

	var expectedFilter3 = []*models.Message{
		{
			Data:         snowplowTsv3,
			PartitionKey: "some-key3",
		},
	}

	filterFunc3, _ := NewSpEnrichedFilterFunction("app_id!=test-data1|test-data2")

	filter3 := NewTransformation(filterFunc3)
	filter3Res := filter3(messages)

	assert.Equal(len(expectedFilter3), len(filter3Res.Result))
	assert.Equal(1, len(filter3Res.Invalid))

}
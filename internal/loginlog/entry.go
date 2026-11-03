// Package loginlog queries the login audit log that ssp-base's
// loginfinalizer:LogToDynamo filter writes to DynamoDB.
package loginlog

import (
	"cmp"
	"time"
)

// TimeLayout is the layout LogToDynamo uses for the Time attribute
// (PHP's 'Y-m-d H:i:s', in GMT per ssp-base's config.php 'timezone').
// It sorts lexically in chronological order, which the scan filter relies on.
const TimeLayout = "2006-01-02 15:04:05"

// Entry is one login record. Attribute names match what LogToDynamo writes.
// The user attributes and Hub are optional: LogToDynamo omits them when empty
// (DynamoDB rejects empty strings), and Hub is only present when the login was
// proxied through a Hub.
type Entry struct {
	ID                     string `dynamodbav:"ID" json:"id"`
	Time                   string `dynamodbav:"Time" json:"time"`
	IDP                    string `dynamodbav:"IDP" json:"idp"`
	SP                     string `dynamodbav:"SP" json:"sp"`
	Hub                    string `dynamodbav:"Hub,omitempty" json:"hub,omitempty"`
	CN                     string `dynamodbav:"CN,omitempty" json:"cn,omitempty"`
	EduPersonPrincipalName string `dynamodbav:"EduPersonPrincipalName,omitempty" json:"eppn,omitempty"`
	EmployeeNumber         string `dynamodbav:"EmployeeNumber,omitempty" json:"employeeNumber,omitempty"`
	ExpiresAt              int64  `dynamodbav:"ExpiresAt,omitempty" json:"expiresAt,omitzero"`
}

// User returns the most stable identifier available for the user who logged in.
func (e Entry) User() string {
	return cmp.Or(e.EmployeeNumber, e.EduPersonPrincipalName, e.CN, "(unknown)")
}

// HubOrDirect returns the Hub entity ID, or "(direct)" when the SP talked to
// the IdP without a Hub in between.
func (e Entry) HubOrDirect() string {
	return cmp.Or(e.Hub, "(direct)")
}

// Day returns the date portion (YYYY-MM-DD) of Time.
func (e Entry) Day() string {
	if len(e.Time) < len(time.DateOnly) {
		return e.Time
	}
	return e.Time[:len(time.DateOnly)]
}

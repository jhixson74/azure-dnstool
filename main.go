package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/Azure/go-autorest/autorest/to"
	"github.com/davecgh/go-spew/spew"

	log "github.com/sirupsen/logrus"

	//azdns "github.com/Azure/azure-sdk-for-go/profiles/latest/dns/mgmt/dns"
	aznetwork "github.com/Azure/azure-sdk-for-go/services/network/mgmt/2018-12-01/network"
	azdns "github.com/Azure/azure-sdk-for-go/services/preview/dns/mgmt/2018-03-01-preview/dns"
	azprivatedns "github.com/Azure/azure-sdk-for-go/services/privatedns/mgmt/2018-09-01/privatedns"
)

type LegacyDNSZoneSerialized struct {
	zone       []byte
	recordsets [][]byte
}

type LegacyDNSZoneInfo struct {
	zone       *azdns.Zone
	recordsets []*azdns.RecordSet
}

type LegacyDNSClient struct {
	resourceGroup    string
	zonesClient      azdns.ZonesClient
	recordsetsClient azdns.RecordSetsClient
}

func NewLegacyDNSClient(session *Session, resourceGroup string) *LegacyDNSClient {
	zonesClient := azdns.NewZonesClient(session.Credentials.SubscriptionID)
	zonesClient.Authorizer = session.Authorizer

	recordsetsClient := azdns.NewRecordSetsClient(session.Credentials.SubscriptionID)
	recordsetsClient.Authorizer = session.Authorizer

	return &LegacyDNSClient{resourceGroup, zonesClient, recordsetsClient}
}

// safe pointer handling for int32
func safeInt32(n *int32) int32 {
	if n == nil {
		return 0
	}
	return *n
}

// safe pointer handling for int64
func safeInt64(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

// safe pointer handling for string
func safeString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// safe pointer handling for array of string
func safeStringArray(s *[]string) string {
	if s == nil {
		return ""
	}
	return (*s)[0]
}

// Takes a subscription ID and parses the resource group out of it
func idToResourceGroup(id string) string {
	parts1 := strings.Split(id, "resourceGroups/")
	if len(parts1) < 2 {
		return ""
	}

	parts2 := strings.Split(parts1[1], "/providers")
	if len(parts2) < 2 {
		return ""
	}

	return parts2[0]
}

// Used to serialize zone and recordset data
func serializeObject(obj interface{}) ([]byte, error) {
	buf := new(bytes.Buffer)
	enc := gob.NewEncoder(buf)
	err := enc.Encode(obj)
	return buf.Bytes(), err
}

// Deserializes into zone and recordsets for legacy DNS zones
func deserializeLegacyZone(sz *LegacyDNSZoneSerialized) (*LegacyDNSZoneInfo, error) {
	szbuf := bytes.NewBuffer(sz.zone)
	zone := new(azdns.Zone)
	zdec := gob.NewDecoder(szbuf)
	zerr := zdec.Decode(&zone)
	if zerr != nil {
		return nil, zerr
	}

	legacyDNSZoneInfo := &LegacyDNSZoneInfo{}
	legacyDNSZoneInfo.zone = zone

	var recordsets []*azdns.RecordSet
	for _, rs := range sz.recordsets {
		srsbuf := bytes.NewBuffer(rs)
		recordset := new(azdns.RecordSet)
		rsdec := gob.NewDecoder(srsbuf)
		rserr := rsdec.Decode(&recordset)
		if rserr != nil {
			return nil, rserr
		}
		recordsets = append(recordsets, recordset)
	}

	legacyDNSZoneInfo.recordsets = recordsets

	return legacyDNSZoneInfo, nil
}

// Deserializes into zone and recordsets for private DNS zones
func deserializePrivateZone(sz *PrivateDNSZoneSerialized) (*PrivateDNSZoneInfo, error) {
	szbuf := bytes.NewBuffer(sz.zone)
	zone := new(azprivatedns.PrivateZone)
	zdec := gob.NewDecoder(szbuf)
	zerr := zdec.Decode(&zone)
	if zerr != nil {
		return nil, zerr
	}

	privateDNSZoneInfo := &PrivateDNSZoneInfo{}
	privateDNSZoneInfo.zone = zone

	var recordsets []*azprivatedns.RecordSet
	for _, rs := range sz.recordsets {
		srsbuf := bytes.NewBuffer(rs)
		recordset := new(azprivatedns.RecordSet)
		rsdec := gob.NewDecoder(srsbuf)
		rserr := rsdec.Decode(&recordset)
		if rserr != nil {
			return nil, rserr
		}
		recordsets = append(recordsets, recordset)
	}

	privateDNSZoneInfo.recordsets = recordsets

	return privateDNSZoneInfo, nil
}

// Gets a single legacy zone and serializes it
func (client *LegacyDNSClient) GetZoneSerialized(legacyZone string) (*LegacyDNSZoneSerialized, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	defer cancel()

	zone, err := client.zonesClient.Get(ctx, client.resourceGroup, legacyZone)
	zone.Response.Response = nil
	if err != nil {
		return nil, err
	}

	if zone.ZoneProperties.ZoneType != azdns.Private {
		return nil, errors.New("not a private zone")
	}

	legacyDNSZoneSerialized := LegacyDNSZoneSerialized{}
	szone, err := serializeObject(zone)
	if err != nil {
		return nil, err
	}
	legacyDNSZoneSerialized.zone = szone

	for recordsetsPage, err := client.recordsetsClient.ListAllByDNSZone(ctx, client.resourceGroup, to.String(zone.Name), to.Int32Ptr(100), ""); recordsetsPage.NotDone(); err = recordsetsPage.NextWithContext(ctx) {
		if err != nil {
			return nil, err
		}

		for _, rs := range recordsetsPage.Values() {
			srs, err := serializeObject(rs)
			if err != nil {
				return nil, err
			}
			legacyDNSZoneSerialized.recordsets = append(legacyDNSZoneSerialized.recordsets, srs)
		}
	}

	return &legacyDNSZoneSerialized, nil
}

// Gets all legacy zones and serializes them
func (client *LegacyDNSClient) GetZonesSerialized() ([]*LegacyDNSZoneSerialized, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	defer cancel()

	var legacyDNSZonesSerialized []*LegacyDNSZoneSerialized
	for zonesPage, err := client.zonesClient.List(ctx, to.Int32Ptr(100)); zonesPage.NotDone(); err = zonesPage.NextWithContext(ctx) {
		if err != nil {
			return nil, err
		}

		for _, zone := range zonesPage.Values() {
			zone.Response.Response = nil
			if zone.ZoneProperties.ZoneType != azdns.Private {
				continue
			}

			szone, err := serializeObject(zone)
			if err != nil {
				return nil, err
			}

			legacyDNSZoneSerialized := LegacyDNSZoneSerialized{}
			legacyDNSZoneSerialized.zone = szone

			resourceGroup := idToResourceGroup(*zone.ID)
			for recordsetsPage, err := client.recordsetsClient.ListAllByDNSZone(ctx, resourceGroup, to.String(zone.Name), to.Int32Ptr(100), ""); recordsetsPage.NotDone(); err = recordsetsPage.NextWithContext(ctx) {
				if err != nil {
					return nil, err
				}

				for _, rs := range recordsetsPage.Values() {
					srs, err := serializeObject(rs)
					if err != nil {
						return nil, err
					}
					legacyDNSZoneSerialized.recordsets = append(legacyDNSZoneSerialized.recordsets, srs)
				}
			}
			legacyDNSZonesSerialized = append(legacyDNSZonesSerialized, &legacyDNSZoneSerialized)
		}
	}

	return legacyDNSZonesSerialized, nil
}

// Create a legacy private zone with mock records
func (client *LegacyDNSClient) CreateZoneWithRecordSets(zoneName string) error {
	location := "global"
	maxNumberOfRecordSets := int64(10000)

	legacyZone := azdns.Zone{}
	legacyZone.Location = &location
	legacyZone.Name = &zoneName
	legacyZone.ZoneProperties = &azdns.ZoneProperties{
		MaxNumberOfRecordSets: &maxNumberOfRecordSets,
		ZoneType:              azdns.Private,
	}

	ctx, cancel := context.WithTimeout(context.TODO(), 300*time.Second)
	defer cancel()

	// Create the Zone
	log.Printf("zone: %s ... ", zoneName)
	_, err := client.zonesClient.CreateOrUpdate(ctx, client.resourceGroup, zoneName, legacyZone, "", "")
	if err != nil {
		return err
	}

	// Read back the newly created zone to verify creation
	_, err = client.zonesClient.Get(ctx, client.resourceGroup, zoneName)
	if err != nil {
		return err
	}
	log.Printf("ok.\n")

	// Create some fake A records
	for i := 1; i <= 10; i++ {
		name := fmt.Sprintf("host%02d", i)
		ttl := int64(60)

		ipv4Address := fmt.Sprintf("10.0.0.%d", i)
		aRecords := []azdns.ARecord{
			azdns.ARecord{&ipv4Address},
		}

		legacyRecordSet := azdns.RecordSet{
			Name: &name,
			RecordSetProperties: &azdns.RecordSetProperties{
				TTL:      &ttl,
				ARecords: &aRecords,
			},
		}

		// Create/Update the record
		log.Printf("record: %s %s ... ", azdns.A, name)
		_, err = client.recordsetsClient.CreateOrUpdate(ctx, client.resourceGroup, zoneName, name, azdns.A, legacyRecordSet, "", "")
		if err != nil {
			return err
		}

		// Read back the newly created record to verify creation
		_, err = client.recordsetsClient.Get(ctx, client.resourceGroup, zoneName, name, azdns.A)
		if err != nil {
			return err
		}
		log.Printf("ok.\n")
	}

	return err
}

type PrivateDNSZoneSerialized struct {
	zone       []byte
	recordsets [][]byte
}

type PrivateDNSZoneInfo struct {
	zone       *azprivatedns.PrivateZone
	recordsets []*azprivatedns.RecordSet
}

type PrivateDNSClient struct {
	resourceGroup             string
	virtualNetwork            string
	zonesClient               azprivatedns.PrivateZonesClient
	recordsetsClient          azprivatedns.RecordSetsClient
	virtualNetworkLinksClient azprivatedns.VirtualNetworkLinksClient
	virtualNetworksClient     aznetwork.VirtualNetworksClient
}

func NewPrivateDNSClient(session *Session, resourceGroup string, virtualNetwork string) *PrivateDNSClient {
	zonesClient := azprivatedns.NewPrivateZonesClient(session.Credentials.SubscriptionID)
	zonesClient.Authorizer = session.Authorizer

	recordsetsClient := azprivatedns.NewRecordSetsClient(session.Credentials.SubscriptionID)
	recordsetsClient.Authorizer = session.Authorizer

	virtualNetworkLinksClient := azprivatedns.NewVirtualNetworkLinksClient(session.Credentials.SubscriptionID)
	virtualNetworkLinksClient.Authorizer = session.Authorizer

	virtualNetworksClient := aznetwork.NewVirtualNetworksClient(session.Credentials.SubscriptionID)
	virtualNetworksClient.Authorizer = session.Authorizer

	return &PrivateDNSClient{resourceGroup, virtualNetwork, zonesClient, recordsetsClient, virtualNetworkLinksClient, virtualNetworksClient}
}

// Gets a single private zone and serializes it
func (client *PrivateDNSClient) GetZoneSerialized(privateZone string) (*PrivateDNSZoneSerialized, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	defer cancel()

	zone, err := client.zonesClient.Get(ctx, client.resourceGroup, privateZone)
	zone.Response.Response = nil
	if err != nil {
		return nil, err
	}

	privateDNSZoneSerialized := PrivateDNSZoneSerialized{}
	szone, err := serializeObject(zone)
	if err != nil {
		return nil, err
	}
	privateDNSZoneSerialized.zone = szone

	for recordsetsPage, err := client.recordsetsClient.List(ctx, client.resourceGroup, to.String(zone.Name), to.Int32Ptr(100), ""); recordsetsPage.NotDone(); err = recordsetsPage.NextWithContext(ctx) {
		if err != nil {
			return nil, err
		}

		for _, rs := range recordsetsPage.Values() {
			srs, err := serializeObject(rs)
			if err != nil {
				return nil, err
			}
			privateDNSZoneSerialized.recordsets = append(privateDNSZoneSerialized.recordsets, srs)
		}
	}

	return &privateDNSZoneSerialized, nil
}

// Gets all private zones and serializes them
func (client *PrivateDNSClient) GetZonesSerialized() ([]*PrivateDNSZoneSerialized, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	defer cancel()

	var privateDNSZonesSerialized []*PrivateDNSZoneSerialized
	for zonesPage, err := client.zonesClient.List(ctx, to.Int32Ptr(100)); zonesPage.NotDone(); err = zonesPage.NextWithContext(ctx) {
		if err != nil {
			return nil, err
		}

		for _, zone := range zonesPage.Values() {
			zone.Response.Response = nil
			szone, err := serializeObject(zone)
			if err != nil {
				return nil, err
			}

			privateDNSZoneSerialized := PrivateDNSZoneSerialized{}
			privateDNSZoneSerialized.zone = szone

			resourceGroup := idToResourceGroup(*zone.ID)
			for recordsetsPage, err := client.recordsetsClient.List(ctx, resourceGroup, to.String(zone.Name), to.Int32Ptr(100), ""); recordsetsPage.NotDone(); err = recordsetsPage.NextWithContext(ctx) {
				if err != nil {
					return nil, err
				}

				for _, rs := range recordsetsPage.Values() {
					srs, err := serializeObject(rs)
					if err != nil {
						return nil, err
					}
					privateDNSZoneSerialized.recordsets = append(privateDNSZoneSerialized.recordsets, srs)
				}
			}
			privateDNSZonesSerialized = append(privateDNSZonesSerialized, &privateDNSZoneSerialized)
		}
	}

	return privateDNSZonesSerialized, nil
}

// convert a legacy SOA record to a private SOA record
func legacySoaRecordToPrivate(legacySoaRecord *azdns.SoaRecord) *azprivatedns.SoaRecord {
	var soaRecord *azprivatedns.SoaRecord = nil

	if legacySoaRecord != nil {
		soaRecord = &azprivatedns.SoaRecord{
			Host:         legacySoaRecord.Host,
			Email:        legacySoaRecord.Email,
			SerialNumber: legacySoaRecord.SerialNumber,
			RefreshTime:  legacySoaRecord.RefreshTime,
			RetryTime:    legacySoaRecord.RetryTime,
			ExpireTime:   legacySoaRecord.ExpireTime,
			MinimumTTL:   legacySoaRecord.MinimumTTL,
		}
	}

	return soaRecord
}

// convert a legacy MX record to a private MX record
func legacyMxRecordToPrivate(legacyMxRecord *azdns.MxRecord) *azprivatedns.MxRecord {
	var mxRecord *azprivatedns.MxRecord = nil

	if legacyMxRecord != nil {
		mxRecord = &azprivatedns.MxRecord{
			Preference: legacyMxRecord.Preference,
			Exchange:   legacyMxRecord.Exchange,
		}
	}

	return mxRecord
}

// convert a legacy A record to a private A record
func legacyARecordToPrivate(legacyARecord *azdns.ARecord) *azprivatedns.ARecord {
	var aRecord *azprivatedns.ARecord = nil

	if legacyARecord != nil {
		aRecord = &azprivatedns.ARecord{
			Ipv4Address: legacyARecord.Ipv4Address,
		}
	}
	return aRecord
}

// convert a legacy AAAA record to a private AAAA record
func legacyAaaaRecordToPrivate(legacyAaaaRecord *azdns.AaaaRecord) *azprivatedns.AaaaRecord {
	var aaaaRecord *azprivatedns.AaaaRecord = nil

	if legacyAaaaRecord != nil {
		aaaaRecord = &azprivatedns.AaaaRecord{
			Ipv6Address: legacyAaaaRecord.Ipv6Address,
		}
	}

	return aaaaRecord
}

// convert a legacy CNAME record to a private CNAME record
func legacyCnameRecordToPrivate(legacyCnameRecord *azdns.CnameRecord) *azprivatedns.CnameRecord {
	var cnameRecord *azprivatedns.CnameRecord = nil

	if legacyCnameRecord != nil {
		cnameRecord = &azprivatedns.CnameRecord{
			Cname: legacyCnameRecord.Cname,
		}
	}

	return cnameRecord
}

// convert a legacy PTR record to a private PTR record
func legacyPtrRecordToPrivate(legacyPtrRecord *azdns.PtrRecord) *azprivatedns.PtrRecord {
	var ptrRecord *azprivatedns.PtrRecord = nil

	if legacyPtrRecord != nil {
		ptrRecord = &azprivatedns.PtrRecord{
			Ptrdname: legacyPtrRecord.Ptrdname,
		}
	}

	return ptrRecord
}

// convert a legacy SRV record to a private SRV record
func legacySrvRecordToPrivate(legacySrvRecord *azdns.SrvRecord) *azprivatedns.SrvRecord {
	var srvRecord *azprivatedns.SrvRecord = nil

	if legacySrvRecord != nil {
		srvRecord = &azprivatedns.SrvRecord{
			Priority: legacySrvRecord.Priority,
			Weight:   legacySrvRecord.Weight,
			Port:     legacySrvRecord.Port,
			Target:   legacySrvRecord.Target,
		}
	}

	return srvRecord
}

// convert a legacy TXT record to a private TXT record
func legacyTxtRecordToPrivate(legacyTxtRecord *azdns.TxtRecord) *azprivatedns.TxtRecord {
	var txtRecord *azprivatedns.TxtRecord = nil

	if legacyTxtRecord != nil {
		txtRecord = &azprivatedns.TxtRecord{
			Value: legacyTxtRecord.Value,
		}
	}

	return txtRecord
}

// convert an array of legacy MX records to an array of private MX records
func legacyMxRecordsToPrivate(legacyMxRecords *[]azdns.MxRecord) *[]azprivatedns.MxRecord {
	var mxRecords []azprivatedns.MxRecord = nil

	if legacyMxRecords != nil {
		mxRecords = make([]azprivatedns.MxRecord, len(*legacyMxRecords))
		for _, legacyMxRecord := range *legacyMxRecords {
			mxRecord := legacyMxRecordToPrivate(&legacyMxRecord)
			if mxRecord != nil {
				mxRecords = append(mxRecords, *mxRecord)
			}
		}
	}

	return &mxRecords
}

// convert an array of legacy A records to an array of private A records
func legacyARecordsToPrivate(legacyARecords *[]azdns.ARecord) *[]azprivatedns.ARecord {
	var aRecords []azprivatedns.ARecord = nil

	if legacyARecords != nil {
		for _, legacyARecord := range *legacyARecords {
			aRecord := legacyARecordToPrivate(&legacyARecord)
			if aRecord != nil {
				aRecords = append(aRecords, *aRecord)
			}

		}
	}

	return &aRecords
}

// convert an array of legacy AAAA records to an array of private AAAA records
func legacyAaaaRecordsToPrivate(legacyAaaaRecords *[]azdns.AaaaRecord) *[]azprivatedns.AaaaRecord {
	var aaaaRecords []azprivatedns.AaaaRecord = nil

	if legacyAaaaRecords != nil {
		for _, legacyAaaaRecord := range *legacyAaaaRecords {
			aaaaRecord := legacyAaaaRecordToPrivate(&legacyAaaaRecord)
			if aaaaRecord != nil {
				aaaaRecords = append(aaaaRecords, *aaaaRecord)
			}
		}
	}

	return &aaaaRecords
}

// convert an array of legacy PTR records to an array of private PTR records
func legacyPtrRecordsToPrivate(legacyPtrRecords *[]azdns.PtrRecord) *[]azprivatedns.PtrRecord {
	var ptrRecords []azprivatedns.PtrRecord = nil

	if legacyPtrRecords != nil {
		for _, legacyPtrRecord := range *legacyPtrRecords {
			ptrRecord := legacyPtrRecordToPrivate(&legacyPtrRecord)
			if ptrRecord != nil {
				ptrRecords = append(ptrRecords, *ptrRecord)
			}
		}
	}

	return &ptrRecords
}

// convert an array of legacy SRV records to an array of private SRV records
func legacySrvRecordsToPrivate(legacySrvRecords *[]azdns.SrvRecord) *[]azprivatedns.SrvRecord {
	var srvRecords []azprivatedns.SrvRecord = nil

	if legacySrvRecords != nil {
		for _, legacySrvRecord := range *legacySrvRecords {
			srvRecord := legacySrvRecordToPrivate(&legacySrvRecord)
			if srvRecord != nil {
				srvRecords = append(srvRecords, *srvRecord)
			}
		}
	}

	return &srvRecords
}

// convert an array of legacy TXT records to an array of private TXT records
func legacyTxtRecordsToPrivate(legacyTxtRecords *[]azdns.TxtRecord) *[]azprivatedns.TxtRecord {
	var txtRecords []azprivatedns.TxtRecord = nil

	if legacyTxtRecords != nil {
		for _, legacyTxtRecord := range *legacyTxtRecords {
			txtRecord := legacyTxtRecordToPrivate(&legacyTxtRecord)
			if txtRecord != nil {
				txtRecords = append(txtRecords, *txtRecord)
			}
		}
	}

	return &txtRecords
}

func isLegacy(object interface{}) bool {
	return strings.Contains(reflect.TypeOf(object).String(), "*dns.")
}
func isPrivate(object interface{}) bool {
	return strings.Contains(reflect.TypeOf(object).String(), "*privatedns.")
}

// print out a legacy SOA record
func showLegacySoaRecord(soaRecord *azdns.SoaRecord, zoneName string, fqdn string, ttl int64) {
	header := fmt.Sprintf("@ SOA %-6d Email: %s", *soaRecord.RefreshTime, *soaRecord.Email)
	index := strings.Index(header, "Email:")

	log.Printf(header)
	log.Printf("%*sHost: %s", index, "", safeString(soaRecord.Host))
	log.Printf("%*sRefresh: %d", index, "", safeInt64(soaRecord.RefreshTime))
	log.Printf("%*sRetry: %d", index, "", safeInt64(soaRecord.RetryTime))
	log.Printf("%*sExpire: %d", index, "", safeInt64(soaRecord.ExpireTime))
	log.Printf("%*sMinimum TTL: %d", index, "", safeInt64(soaRecord.MinimumTTL))
	log.Printf("%*sSerial: %d", index, "", safeInt64(soaRecord.SerialNumber))
}

// print out a legacy CNAME record
func showLegacyCnameRecord(cnameRecord *azdns.CnameRecord, zoneName string, fqdn string, ttl int64) {
	shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)

	log.Printf("%s CNAME %d %s", shortName, ttl, safeString(cnameRecord.Cname))
}

// print out legacy A records
func showLegacyARecords(aRecords *[]azdns.ARecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, aRecord := range *aRecords {
		ipv4Address := safeString(aRecord.Ipv4Address)
		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s A %-6d %s", shortName, ttl, ipv4Address)
			index = strings.Index(header, ipv4Address)
			log.Printf(header)
		} else {
			log.Printf("%*s%s", index, "", ipv4Address)
		}
		count++
	}
}

// print out legacy AAAA records
func showLegacyAaaaRecords(aaaaRecords *[]azdns.AaaaRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, aaaaRecord := range *aaaaRecords {
		ipv6Address := safeString(aaaaRecord.Ipv6Address)
		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s AAAA %-6d %s", shortName, ttl, ipv6Address)
			index = strings.Index(header, ipv6Address)
			log.Printf(header)
		} else {
			log.Printf("%*s%s", index, "", ipv6Address)
		}
		count++
	}
}

// print out legacy MX records
func showLegacyMxRecords(mxRecords *[]azdns.MxRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, mxRecord := range *mxRecords {
		exchange := safeString(mxRecord.Exchange)
		preference := safeInt32(mxRecord.Preference)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s MX %-6d %-6d %s", shortName, ttl, preference, exchange)
			index = strings.Index(header, fmt.Sprintf("%d", preference))
			log.Printf(header)
		} else {
			log.Printf("%*s%-6d %s", index, "", preference, exchange)
		}
		count++
	}
}

// print out legacy PTR records
func showLegacyPtrRecords(ptrRecords *[]azdns.PtrRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, ptrRecord := range *ptrRecords {
		ptrdname := safeString(ptrRecord.Ptrdname)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s PTR %s", shortName, ptrdname)
			index = strings.Index(header, ptrdname)
			log.Printf(header)
		} else {
			log.Printf("%*s%s", index, "", ptrdname)
		}
		count++
	}
}

// print out legacy SRV records
func showLegacySrvRecords(srvRecords *[]azdns.SrvRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, srvRecord := range *srvRecords {
		priority := safeInt32(srvRecord.Priority)
		weight := safeInt32(srvRecord.Weight)
		port := safeInt32(srvRecord.Port)
		target := safeString(srvRecord.Target)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s SRV %-6d %-6d %-6d %-6d %s", shortName, ttl, priority, weight, port, target)
			index = strings.Index(header, fmt.Sprintf("%d", *srvRecord.Priority))
			log.Printf(header)
		} else {
			log.Printf("%*s%-6d %-6d %-6d %s", index, "", priority, weight, port, target)
		}
		count++
	}
}

// print out legacy TXT records
func showLegacyTxtRecords(txtRecords *[]azdns.TxtRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, txtRecord := range *txtRecords {
		value := safeStringArray(txtRecord.Value)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s TXT %s", shortName, value)
			index = strings.Index(header, value)
			log.Printf(header)
		} else {
			log.Printf("%*s%s", index, "", value)
		}
		count++
	}
}

// print out a private SOA record
func showPrivateSoaRecord(soaRecord *azprivatedns.SoaRecord, zoneName string, fqdn string, ttl int64) {
	header := fmt.Sprintf("@ SOA %-6d Email: %s", *soaRecord.RefreshTime, *soaRecord.Email)
	index := strings.Index(header, "Email:")

	log.Printf(header)
	log.Printf("%*sHost: %s", index, "", safeString(soaRecord.Host))
	log.Printf("%*sRefresh: %d", index, "", safeInt64(soaRecord.RefreshTime))
	log.Printf("%*sRetry: %d", index, "", safeInt64(soaRecord.RetryTime))
	log.Printf("%*sExpire: %d", index, "", safeInt64(soaRecord.ExpireTime))
	log.Printf("%*sMinimum TTL: %d", index, "", safeInt64(soaRecord.MinimumTTL))
	log.Printf("%*sSerial: %d", index, "", safeInt64(soaRecord.SerialNumber))
}

// print out a private CNAME record
func showPrivateCnameRecord(cnameRecord *azprivatedns.CnameRecord, zoneName string, fqdn string, ttl int64) {
	shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)

	log.Printf("%s CNAME %d %s", shortName, ttl, safeString(cnameRecord.Cname))
}

// print out private A records
func showPrivateARecords(aRecords *[]azprivatedns.ARecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, aRecord := range *aRecords {
		ipv4Address := safeString(aRecord.Ipv4Address)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s A %-6d %s", shortName, ttl, ipv4Address)
			index = strings.Index(header, ipv4Address)
			log.Printf(header)
		} else {
			log.Printf("%*s%s", index, "", ipv4Address)
		}
		count++
	}
}

// print out private AAAA records
func showPrivateAaaaRecords(aaaaRecords *[]azprivatedns.AaaaRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, aaaaRecord := range *aaaaRecords {
		ipv6Address := safeString(aaaaRecord.Ipv6Address)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s AAAA %-6d %s", shortName, ttl, ipv6Address)
			index = strings.Index(header, ipv6Address)
			log.Printf(header)
		} else {
			log.Printf("%*s%s", index, "", ipv6Address)
		}
		count++
	}
}

// print out private MX records
func showPrivateMxRecords(mxRecords *[]azprivatedns.MxRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, mxRecord := range *mxRecords {
		preference := safeInt32(mxRecord.Preference)
		exchange := safeString(mxRecord.Exchange)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s MX %-6d %-6d %s", shortName, ttl, preference, exchange)
			index = strings.Index(header, fmt.Sprintf("%d", preference))
			log.Printf(header)
		} else {
			log.Printf("%*s%-6d %s", index, "", preference, exchange)
		}
		count++
	}
}

// print out private PTR records
func showPrivatePtrRecords(ptrRecords *[]azprivatedns.PtrRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, ptrRecord := range *ptrRecords {
		ptrdname := safeString(ptrRecord.Ptrdname)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s PTR %s", shortName, ptrdname)
			index = strings.Index(header, ptrdname)
			log.Printf(header)
		} else {
			log.Printf("%*s%s", index, "", ptrdname)
		}
		count++
	}
}

// print out private SRV records
func showPrivateSrvRecords(srvRecords *[]azprivatedns.SrvRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, srvRecord := range *srvRecords {
		priority := safeInt32(srvRecord.Priority)
		weight := safeInt32(srvRecord.Weight)
		port := safeInt32(srvRecord.Port)
		target := safeString(srvRecord.Target)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s SRV %-6d %-6d %-6d %-6d %s", shortName, ttl, priority, weight, port, target)
			index = strings.Index(header, fmt.Sprintf("%d", priority))
			log.Printf(header)
		} else {
			log.Printf("%*s%-6d %-6d %-6d %s", index, "", priority, weight, port, target)
		}
		count++
	}
}

// print out private TXT records
func showPrivateTxtRecords(txtRecords *[]azprivatedns.TxtRecord, zoneName string, fqdn string, ttl int64) {
	var count, index int = 0, 0

	for _, txtRecord := range *txtRecords {
		value := safeStringArray(txtRecord.Value)

		if count == 0 {
			shortName := strings.Replace(fqdn, "."+zoneName+".", "", 0)
			header := fmt.Sprintf("%s TXT %s", shortName, value)
			index = strings.Index(header, value)
			log.Printf(header)
		} else {
			log.Printf("%*s%s", index, "", value)
		}
		count++
	}
}

// Transforms a legacy zone to a private zone
func (client *PrivateDNSClient) MigrateLegacyZone(legacyDNSZoneInfo *LegacyDNSZoneInfo, link bool) error {
	legacyZone := legacyDNSZoneInfo.zone

	// Setup the private zone to create
	privateZone := azprivatedns.PrivateZone{}
	privateZone.Tags = legacyZone.Tags
	privateZone.Location = legacyZone.Location
	privateZone.Name = legacyZone.Name
	privateZone.PrivateZoneProperties = &azprivatedns.PrivateZoneProperties{
		MaxNumberOfRecordSets: legacyZone.ZoneProperties.MaxNumberOfRecordSets,
	}

	legacyRecordSets := legacyDNSZoneInfo.recordsets

	// Setup the associated recordsets to create
	privateRecordSets := []*azprivatedns.RecordSet{}
	for _, legacyRecordSet := range legacyRecordSets {
		recordType := strings.Replace(*legacyRecordSet.Type, "/dnszones/", "/privateDnsZones/", 0)

		privateRecordSet := azprivatedns.RecordSet{
			Name: legacyRecordSet.Name,
			RecordSetProperties: &azprivatedns.RecordSetProperties{
				Metadata:    legacyRecordSet.Metadata,
				TTL:         legacyRecordSet.TTL,
				Fqdn:        legacyRecordSet.Fqdn,
				SoaRecord:   legacySoaRecordToPrivate(legacyRecordSet.SoaRecord),
				CnameRecord: legacyCnameRecordToPrivate(legacyRecordSet.CnameRecord),
				MxRecords:   legacyMxRecordsToPrivate(legacyRecordSet.MxRecords),
				ARecords:    legacyARecordsToPrivate(legacyRecordSet.ARecords),
				AaaaRecords: legacyAaaaRecordsToPrivate(legacyRecordSet.AaaaRecords),
				PtrRecords:  legacyPtrRecordsToPrivate(legacyRecordSet.PtrRecords),
				SrvRecords:  legacySrvRecordsToPrivate(legacyRecordSet.SrvRecords),
				TxtRecords:  legacyTxtRecordsToPrivate(legacyRecordSet.TxtRecords),
			},
			Type: &recordType,
		}
		privateRecordSets = append(privateRecordSets, &privateRecordSet)
	}

	ctx, cancel := context.WithTimeout(context.TODO(), 300*time.Second)
	defer cancel()

	// Create/Update the Zone
	log.Printf("zone: %s ... ", *privateZone.Name)
	zoneFuture, err := client.zonesClient.CreateOrUpdate(ctx, client.resourceGroup, *privateZone.Name, privateZone, "", "")
	if err != nil {
		return err
	}

	// Wait for zone creation to complete
	err = zoneFuture.WaitForCompletionRef(ctx, client.zonesClient.Client)
	if err != nil {
		return err
	}

	// Read back the newly created zone to verify creation
	_, err = client.zonesClient.Get(ctx, client.resourceGroup, *privateZone.Name)
	if err != nil {
		return err
	}
	log.Printf("ok.\n")

	for _, recordSet := range privateRecordSets {
		recordType := azprivatedns.RecordType(strings.TrimPrefix(*recordSet.Type, "Microsoft.Network/privateDnsZones/"))
		relativeRecordSetName := *recordSet.Name
		recordSet.Type = nil

		// Create/Update the record
		log.Printf("record: %s %s ... ", recordType, relativeRecordSetName)
		_, err := client.recordsetsClient.CreateOrUpdate(ctx, client.resourceGroup, *privateZone.Name, recordType, relativeRecordSetName, *recordSet, "", "")
		if err != nil {
			return err
		}

		// Read back the newly created record to verify creation
		_, err = client.recordsetsClient.Get(ctx, client.resourceGroup, *privateZone.Name, recordType, relativeRecordSetName)
		if err != nil {
			return err
		}
		log.Printf("ok.\n")
	}

	// Do we link, or not?
	if link == false || client.virtualNetwork == "" {
		return nil
	}

	// Get the virtual network so we have some parameters for the link creation
	virtualNetwork, err := client.virtualNetworksClient.Get(ctx, client.resourceGroup, client.virtualNetwork, "")
	if err != nil {
		return err
	}

	location := "global"
	virtualNetworkID := *virtualNetwork.ID
	virtualNetworkLinkName := fmt.Sprintf("%s-network-link", strings.Replace(client.resourceGroup, "-rg", "", 0))

	// pass a flag for this?
	registrationEnabled := false

	virtualNetworkLink := azprivatedns.VirtualNetworkLink{
		Location: &location,
		VirtualNetworkLinkProperties: &azprivatedns.VirtualNetworkLinkProperties{
			VirtualNetwork: &azprivatedns.SubResource{
				ID: &virtualNetworkID,
			},
			RegistrationEnabled: &registrationEnabled,
		},
	}

	// Create the virtual network link to DNS
	log.Printf("link: %s ... ", virtualNetworkLinkName)
	linkFuture, err := client.virtualNetworkLinksClient.CreateOrUpdate(ctx, client.resourceGroup, *privateZone.Name, virtualNetworkLinkName, virtualNetworkLink, "", "")
	if err != nil {
		return err
	}

	// Wait for the link creation to complete
	if err = linkFuture.WaitForCompletionRef(ctx, client.virtualNetworkLinksClient.Client); err != nil {
		return err
	}

	// Read back the newly created link to verify creation
	_, err = client.virtualNetworkLinksClient.Get(ctx, client.resourceGroup, *privateZone.Name, virtualNetworkLinkName)
	if err != nil {
		return err
	}
	log.Printf("ok.\n")

	return nil
}

// Prints out a single legacy zone
func doListLegacyZoneDeserialized(legacyDNSZoneInfo *LegacyDNSZoneInfo) {
	log.Debug(spew.Sdump(legacyDNSZoneInfo))

	zoneName := safeString(legacyDNSZoneInfo.zone.Name)

	log.Printf("LegacyZone: %s", zoneName)
	for _, rs := range legacyDNSZoneInfo.recordsets {
		ttl := int64(30)
		if rs.TTL != nil {
			ttl = *rs.TTL
		}

		if rs.SoaRecord != nil {
			showLegacySoaRecord(rs.SoaRecord, zoneName, *rs.Fqdn, ttl)
		}
		if rs.MxRecords != nil && len(*rs.MxRecords) > 0 {
			showLegacyMxRecords(rs.MxRecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.CnameRecord != nil {
			showLegacyCnameRecord(rs.CnameRecord, zoneName, *rs.Fqdn, ttl)
		}
		if rs.SrvRecords != nil && len(*rs.SrvRecords) > 0 {
			showLegacySrvRecords(rs.SrvRecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.TxtRecords != nil && len(*rs.TxtRecords) > 0 {
			showLegacyTxtRecords(rs.TxtRecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.ARecords != nil && len(*rs.ARecords) > 0 {
			showLegacyARecords(rs.ARecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.AaaaRecords != nil && len(*rs.AaaaRecords) > 0 {
			showLegacyAaaaRecords(rs.AaaaRecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.PtrRecords != nil && len(*rs.PtrRecords) > 0 {
			showLegacyPtrRecords(rs.PtrRecords, zoneName, *rs.Fqdn, ttl)
		}
	}
}

// Retrives legacy zone info and prints it out
func doListLegacyZone(resourceGroup string, legacyZone string) error {
	session, err := GetSession()
	if err != nil {
		return err
	}

	legacyDNSClient := NewLegacyDNSClient(session, resourceGroup)

	// Serialize/Deserialize to make deep copy
	szone, err := legacyDNSClient.GetZoneSerialized(legacyZone)
	if err != nil {
		return err
	}

	legacyDNSZoneInfo, err := deserializeLegacyZone(szone)
	if err != nil {
		return err
	}

	doListLegacyZoneDeserialized(legacyDNSZoneInfo)
	return nil
}

// Prints out a single private zone
func doListPrivateZoneDeserialized(privateDNSZoneInfo *PrivateDNSZoneInfo) {
	log.Debug(spew.Sdump(privateDNSZoneInfo))

	zoneName := safeString(privateDNSZoneInfo.zone.Name)

	log.Printf("PrivateZone: %s", zoneName)
	for _, rs := range privateDNSZoneInfo.recordsets {
		ttl := int64(30)
		if rs.TTL != nil {
			ttl = *rs.TTL
		}

		if rs.SoaRecord != nil {
			showPrivateSoaRecord(rs.SoaRecord, zoneName, *rs.Fqdn, ttl)
		}
		if rs.MxRecords != nil && len(*rs.MxRecords) > 0 {
			showPrivateMxRecords(rs.MxRecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.CnameRecord != nil {
			showPrivateCnameRecord(rs.CnameRecord, zoneName, *rs.Fqdn, ttl)
		}
		if rs.SrvRecords != nil && len(*rs.SrvRecords) > 0 {
			showPrivateSrvRecords(rs.SrvRecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.TxtRecords != nil && len(*rs.TxtRecords) > 0 {
			showPrivateTxtRecords(rs.TxtRecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.ARecords != nil && len(*rs.ARecords) > 0 {
			showPrivateARecords(rs.ARecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.AaaaRecords != nil && len(*rs.AaaaRecords) > 0 {
			showPrivateAaaaRecords(rs.AaaaRecords, zoneName, *rs.Fqdn, ttl)
		}
		if rs.PtrRecords != nil && len(*rs.PtrRecords) > 0 {
			showPrivatePtrRecords(rs.PtrRecords, zoneName, *rs.Fqdn, ttl)
		}
	}

}

// Retrives private zone info and prints it out
func doListPrivateZone(resourceGroup string, privateZone string) error {
	session, err := GetSession()
	if err != nil {
		return err
	}

	privateDNSClient := NewPrivateDNSClient(session, resourceGroup, "")

	// Serialize/Deserialize to make deep copy
	szone, err := privateDNSClient.GetZoneSerialized(privateZone)
	if err != nil {
		return err
	}

	privateDNSZoneInfo, err := deserializePrivateZone(szone)
	if err != nil {
		return err
	}

	doListPrivateZoneDeserialized(privateDNSZoneInfo)
	return nil
}

// Print all legacy zones
func doListAllLegacyZones() error {
	session, err := GetSession()
	if err != nil {
		return err
	}

	legacyDNSClient := NewLegacyDNSClient(session, "")

	szones, err := legacyDNSClient.GetZonesSerialized()
	if err != nil {
		return err
	}

	for _, szone := range szones {
		legacyDNSZoneInfo, err := deserializeLegacyZone(szone)
		if err != nil {
			return err
		}
		doListLegacyZoneDeserialized(legacyDNSZoneInfo)
	}

	return nil
}

// Print all private zones
func doListAllPrivateZones() error {
	session, err := GetSession()
	if err != nil {
		return err
	}

	privateDNSClient := NewPrivateDNSClient(session, "", "")

	szones, err := privateDNSClient.GetZonesSerialized()
	if err != nil {
		return err
	}

	for _, szone := range szones {
		privateDNSZoneInfo, err := deserializePrivateZone(szone)
		if err != nil {
			return err
		}
		doListPrivateZoneDeserialized(privateDNSZoneInfo)
	}

	return nil
}

// Print all legacy and private zones
func doListAllZones() error {
	var err error = nil

	err = doListAllLegacyZones()
	if err != nil {
		return err
	}

	err = doListAllPrivateZones()
	if err != nil {
		return err
	}

	return nil
}

// Do a migration from a legacy zone to a private zone
func doMigrate(oldResourceGroup string, newResourceGroup string, migrateZone string, virtualNetwork string, link bool) error {
	session, err := GetSession()
	if err != nil {
		return err
	}

	if newResourceGroup == "" {
		newResourceGroup = oldResourceGroup
	}

	legacyDNSClient := NewLegacyDNSClient(session, oldResourceGroup)
	privateDNSClient := NewPrivateDNSClient(session, newResourceGroup, virtualNetwork)

	// serialize and deserialize to make deep copies
	szone, err := legacyDNSClient.GetZoneSerialized(migrateZone)
	if err != nil {
		return err
	}

	legacyDNSZoneInfo, err := deserializeLegacyZone(szone)
	if err != nil {
		return err
	}

	// create new private zone
	err = privateDNSClient.MigrateLegacyZone(legacyDNSZoneInfo, link)
	if err != nil {
		return err
	}

	return nil
}

// Show legacy zones that are eligible for migrating to private zones
func doEligible() error {
	session, err := GetSession()
	if err != nil {
		return err
	}

	legacyDNSClient := NewLegacyDNSClient(session, "")

	szones, err := legacyDNSClient.GetZonesSerialized()
	if err != nil {
		return err
	}

	for _, szone := range szones {
		legacyDNSZoneInfo, err := deserializeLegacyZone(szone)
		if err != nil {
			return err
		}

		eligibleZone := legacyDNSZoneInfo.zone
		resourceGroup := idToResourceGroup(*eligibleZone.ID)

		log.Printf("legacy zone=%s resourceGroup=%s\n", *eligibleZone.Name, resourceGroup)
	}

	return nil
}

// Create a legacy private zone with test records
func doTest(zoneName string, resourceGroup string) error {
	session, err := GetSession()
	if err != nil {
		return err
	}

	legacyDNSClient := NewLegacyDNSClient(session, resourceGroup)
	err = legacyDNSClient.CreateZoneWithRecordSets(zoneName)
	if err != nil {
		return err
	}

	return nil
}

func usage() {
	var usageString string = `
Usage: %s [-verbose] [-debug] <command> [arguments]...
Where command is:

    eligible
    list        -resourceGroup=rg [-legacyZone=lz |-privateZone=pz] [-all]
    migrate     -newResourceGroup=rg -zone=z [-oldResourceGroup=rg] [-virtualNetwork=vnet] [-link]
    test        -resourceGroup=rg -zone=z

Arguments:

    # Show legacy zones that are eligible to be migrated
    eligible

        # No arguments

    # list a zone(s)
    list

        # Show all zones and associated records, no other arguments are required
        -all

        # Show a legacy zone and records, requires a resource group
        -legacyZone=zone

        # Show a private zone and records, requires a resource group
        -privateZone=zone

        # The resource group for the specified zone
        -resourceGroup

    # migrate a zone
    migrate

        # The zone we want to migrate (required)
        -zone=example.com

        # The resource group to create the private zone in (required)
        -newResourceGroup=rg

        # The resource group of the legacy zone (optional)
        -oldResourceGroup=rg

        # The virtual network to create the private zone in (optional)
        -virtualNetwork=vnet

        # Link the newly created private zone to the virtual network for DNS (optional)
        -link=vnet

    # create a legacy zone for testing
    test

        # The resource group to create the legacy zone in (required)
        -resourceGroup=rg

        # The name of the legacy zone (required)
        -zone=example.com

Examples:

    # This will show legacy zones that can be migrated to private zones
    azure-dnstool eligible

    # This will show the legacy zone example.com
    azure-dnstool list -legacyZone=example.com -resourceGroup=rg

    # This will show the private zone example.com
    azure-dnstool list -privateZone=example.com -resourceGroup=rg

    # This will show the private zone example.com with debug output
    azure-dnstool -debug list -privateZone=example.com -resourceGroup=rg

    # This will migrate a legacy zone to a private zone
    # Without specifying -link, a virtual network link from the new zone to the vnet will not be created
    azure-dnstool migrate -oldResourceGroup=rg -newResourceGroup=rg -zone=example.com -virtualNetwork=myvnet -link

    # This will create a legacy zone with 10 A records
    azure-dnstool test -resourceGroup=rg -zone=example.com

`
	fmt.Fprintf(os.Stderr, usageString, os.Args[0])
	os.Exit(1)
}

func cmdList(flagArgs []string) error {
	listCmd := flag.NewFlagSet("list", flag.ExitOnError)
	listLegacyZone := listCmd.String("legacyZone", "", "legacyZone")
	listPrivateZone := listCmd.String("privateZone", "", "privateZone")
	listResourceGroup := listCmd.String("resourceGroup", "", "resourceGroup")
	listAll := listCmd.Bool("all", false, "all")

	listCmd.Parse(flagArgs[1:])
	if *listAll == true {
		return doListAllZones()
	}

	// We can probably remove the need for the resource group
	if (*listLegacyZone == "" && *listPrivateZone == "") || *listResourceGroup == "" {
		usage()
	}

	if *listLegacyZone != "" {
		return doListLegacyZone(*listResourceGroup, *listLegacyZone)
	}

	return doListPrivateZone(*listResourceGroup, *listPrivateZone)
}

func cmdMigrate(flagArgs []string) error {
	migrateCmd := flag.NewFlagSet("migrate", flag.ExitOnError)
	migrateZone := migrateCmd.String("zone", "", "zone")
	migrateOldResourceGroup := migrateCmd.String("oldResourceGroup", "", "oldResourceGroup")
	migrateNewResourceGroup := migrateCmd.String("newResourceGroup", "", "newResourceGroup")
	migrateVirtualNetwork := migrateCmd.String("virtualNetwork", "", "virtualNetwork")
	migrateLink := migrateCmd.Bool("link", false, "link")
	//migrateInteractive := migrateCmd.Bool("interactive", false, "interactive")

	migrateCmd.Parse(flagArgs[1:])
	if (*migrateZone == "" || *migrateOldResourceGroup == "") || (*migrateLink == true && *migrateVirtualNetwork == "") {
		usage()
	}

	return doMigrate(*migrateOldResourceGroup, *migrateNewResourceGroup, *migrateZone, *migrateVirtualNetwork, *migrateLink)
}

func cmdEligible(flagArgs []string) error {
	eligibleCmd := flag.NewFlagSet("eligible", flag.ExitOnError)
	eligibleCmd.Parse(flagArgs[1:])

	return doEligible()
}

func cmdTest(flagArgs []string) error {
	testCmd := flag.NewFlagSet("test", flag.ExitOnError)
	testZone := testCmd.String("zone", "", "zone")
	testResourceGroup := testCmd.String("resourceGroup", "", "resourceGroup")

	testCmd.Parse(flagArgs[1:])
	if *testZone == "" || *testResourceGroup == "" {
		usage()
	}

	return doTest(*testZone, *testResourceGroup)
}

func main() {
	var err error

	if len(os.Args) < 2 {
		usage()
	}

	debug := flag.Bool("debug", false, "debug")
	verbose := flag.Bool("verbose", false, "verbose")

	flag.Parse()

	log.SetLevel(log.ErrorLevel)
	if *verbose == true {
		log.SetLevel(log.InfoLevel)
	}
	if *debug == true {
		log.SetLevel(log.DebugLevel)
	}

	flagArgs := flag.Args()
	switch flagArgs[0] {
	case "eligible":
		if *debug != true {
			log.SetLevel(log.InfoLevel)
		}
		err = cmdEligible(flagArgs)

	case "list":
		if *debug != true {
			log.SetLevel(log.InfoLevel)
		}
		err = cmdList(flagArgs)

	case "migrate":
		err = cmdMigrate(flagArgs)

	case "test":
		err = cmdTest(flagArgs)

	default:
		usage()
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	return
}

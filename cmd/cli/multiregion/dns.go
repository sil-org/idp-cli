/*
Copyright © 2023 SIL International
*/

package multiregion

import (
	"context"
	"fmt"
	"log"

	"github.com/cloudflare/cloudflare-go"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/silinternational/idp-cli/cmd/cli/flags"
)

type DnsCommand struct {
	cfClient      *cloudflare.API
	cfZone        *cloudflare.ResourceContainer
	domainName    string
	env           string
	failback      bool
	includeCommon bool
	region        string
	region2       string
	testMode      bool
}

func InitDnsCmd(parentCmd *cobra.Command) {
	var failback, includeCommon bool

	cmd := &cobra.Command{
		Use:   "dns",
		Short: "DNS Failover and Failback",
		Long:  `Configure DNS CNAME values for primary or secondary region hostnames. Default is failover, use --failback to switch back to the primary region.`,
		Run: func(cmd *cobra.Command, args []string) {
			runDnsCommand(failback, includeCommon)
		},
	}
	parentCmd.AddCommand(cmd)

	cmd.PersistentFlags().BoolVar(&failback, "failback", false,
		`set DNS records to switch back to primary`,
	)
	cmd.PersistentFlags().BoolVar(&includeCommon, "include-common", false,
		`also set DNS records for services used by every IdP`,
	)
}

func runDnsCommand(failback, includeCommon bool) {
	pFlags := getPersistentFlags()

	if pFlags.readOnlyMode {
		fmt.Println("-- Read-only mode enabled --")
	}

	d := newDnsCommand(pFlags, failback, includeCommon)

	d.setDnsRecordValues(pFlags.idp)
}

func newDnsCommand(pFlags PersistentFlags, failback, includeCommon bool) *DnsCommand {
	d := DnsCommand{
		testMode:   pFlags.readOnlyMode,
		domainName: viper.GetString(flags.DomainName),
	}

	if d.domainName == "" {
		log.Fatalln("Cloudflare Domain Name is not configured. Use 'domain-name' parameter.")
	}

	cfToken := viper.GetString("cloudflare-token")
	if cfToken == "" {
		log.Fatalln("Cloudflare Token is not configured. Use 'cloudflare-token' parameter.")
	}

	api, err := cloudflare.NewWithAPIToken(cfToken)
	if err != nil {
		log.Fatal("failed to initialize the Cloudflare API:", err)
	}
	d.cfClient = api

	zoneID, err := d.cfClient.ZoneIDByName(d.domainName)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Using domain name %s with ID %s\n", d.domainName, zoneID)
	d.cfZone = cloudflare.ZoneIdentifier(zoneID)

	d.env = pFlags.env
	d.region = pFlags.region
	d.region2 = pFlags.secondaryRegion

	d.failback = failback
	d.includeCommon = includeCommon

	return &d
}

func (d *DnsCommand) setDnsRecordValues(idpKey string) {
	if d.failback {
		fmt.Println("Setting DNS records to primary region...")
	} else {
		fmt.Println("Setting DNS records to secondary region...")
	}

	region := d.region2
	if d.failback {
		region = d.region
	}

	supportBotName := "sherlock"
	if d.env != envProd {
		supportBotName = "watson"
	}

	type nameValuePair struct {
		name  string
		value string
	}

	dnsRecords := []nameValuePair{
		// ECS services
		{idpKey + "-pw-api", idpKey + "-pw-api-" + region},
		{idpKey, idpKey + "-" + region},
	}

	common := []nameValuePair{
		// "twosv-api" is the TOTP and Webauthn API, also known as serverless-mfa-api-go
		{"twosv-api", "twosv-api-" + region},

		// this is the idp-support-bot API that is configured in the Slack API dashboard
		{supportBotName, supportBotName + "-" + region},
	}

	if d.includeCommon {
		dnsRecords = append(dnsRecords, common...)
	}

	for _, record := range dnsRecords {
		d.setCloudflareCname(record.name, record.value+"."+d.domainName)
	}
}

func (d *DnsCommand) setCloudflareCname(name, value string) {
	if value == "" {
		fmt.Printf("  skipping %s (no value provided)\n", name)
		return
	}

	fmt.Printf("  %s.%s --> %s\n", name, d.domainName, value)

	ctx := context.Background()

	r, _, err := d.cfClient.ListDNSRecords(ctx, d.cfZone, cloudflare.ListDNSRecordsParams{Name: name + "." + d.domainName})
	if err != nil {
		fmt.Printf("Error: Cloudflare API call failed to find DNS record %s: %s\n", name, err)
		return
	}
	if len(r) != 1 {
		fmt.Printf("Error: did not find DNS record %q in domain %q\n", name, d.domainName)
		return
	}

	if r[0].Content == value {
		fmt.Printf("CNAME %s is already set to %s\n", name, value)
		return
	}

	if d.testMode {
		fmt.Println("  read-only mode: skipping API call")
		return
	}

	answer := simplePrompt(`Type "yes" to set this DNS record`)
	if answer != "yes" {
		return
	}

	_, err = d.cfClient.UpdateDNSRecord(ctx, d.cfZone, cloudflare.UpdateDNSRecordParams{
		ID:      r[0].ID,
		Type:    "CNAME",
		Name:    name,
		Content: value,
		Comment: cloudflare.StringPtr(r[0].Comment),
	})
	if err != nil {
		fmt.Printf("error updating DNS record %s: %s\n", name, err)
	}
}

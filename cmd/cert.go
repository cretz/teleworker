package cmd

import (
	"fmt"
	"log"
	"os"

	"github.com/cretz/teleworker/workergrpc"
	"github.com/spf13/cobra"
)

func genCertCmd() *cobra.Command {
	var signerCert, signerKey string
	var config workergrpc.GenerateCertificateConfig
	cmd := &cobra.Command{
		Use:          "gen-cert FILENAME_SANS_EXT",
		Short:        "Generate auth or CA certificate for server or client",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if config.CA && config.ServerHost != "" {
				return fmt.Errorf("cannot have server host for CA")
			}
			// Load signer and key
			if signerCert != "" || signerKey != "" {
				if signerCert == "" || signerKey == "" {
					return fmt.Errorf("must have both or neither signer cert and key")
				}
				var err error
				if config.SignerCert, err = os.ReadFile(signerCert); err != nil {
					return fmt.Errorf("reading signer cert: %w", err)
				}
				if config.SignerKey, err = os.ReadFile(signerKey); err != nil {
					return fmt.Errorf("reading signer key: %w", err)
				}
			}
			// Generate cert and save
			certBytes, keyBytes, err := workergrpc.GenerateCertificate(config)
			if err != nil {
				return err
			}
			log.Printf("Writing certificate to %v", args[0]+".crt")
			if err := os.WriteFile(args[0]+".crt", certBytes, 0644); err != nil {
				return fmt.Errorf("writing cert: %w", err)
			}
			log.Printf("Writing key to %v", args[0]+".key")
			if err := os.WriteFile(args[0]+".key", keyBytes, 0600); err != nil {
				return fmt.Errorf("writing key: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&signerCert, "signer-cert", "", "Certification file to sign with")
	cmd.Flags().StringVar(&signerKey, "signer-key", "", "Key file to sign with")
	cmd.Flags().BoolVar(&config.CA, "is-ca", false, "Make a signer/CA certificate")
	cmd.Flags().StringVar(&config.OU, "ou", "", "Set the OU which is used as the job namespace")
	cmd.Flags().StringVar(&config.ServerHost, "server-host", "", "Make a server auth certificate with this IP or DNS name")
	return cmd
}

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/smart-bit-me/scomp-mcp/internal/api"
	"github.com/smart-bit-me/scomp-mcp/internal/credentials"
	"github.com/smart-bit-me/scomp-mcp/internal/mcpserver"
	"github.com/smart-bit-me/scomp-mcp/internal/terminal"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "scomp-mcp:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "login":
		return login(args[1:])
	case "logout":
		return logout(args[1:])
	case "status":
		return status(args[1:])
	case "serve":
		return serve(args[1:])
	case "version", "--version", "-version":
		fmt.Println("scomp-mcp", version)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return usageError()
	}
}

func login(args []string) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	serverURL := flags.String("server", envOr(credentials.EnvServer, api.DefaultServerURL), "scomp relay origin")
	label := flags.String("label", defaultLabel(), "device label shown during approval")
	readOnly := flags.Bool("read-only", false, "request terminal read access without input access")
	noBrowser := flags.Bool("no-browser", false, "print the approval URL without opening a browser")
	configPath := flags.String("config", "", "credential file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("login accepts flags only")
	}
	client, err := api.NewClient(*serverURL, "")
	if err != nil {
		return err
	}
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	scopes := []api.Scope{api.ScopeHostsRead, api.ScopeTerminalRead}
	if !*readOnly {
		scopes = append(scopes, api.ScopeTerminalWrite)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	started, err := client.StartDevice(ctx, *label, scopes, challenge)
	if err != nil {
		var apiError *api.APIError
		if errors.As(err, &apiError) && apiError.Status == 404 {
			return errors.New("relay does not provide MCP authorization yet; deploy the MCP-enabled scomp-server or select it with --server")
		}
		if errors.As(err, &apiError) && apiError.Status == 429 {
			return errors.New("relay rate limit reached; wait a minute and run login again")
		}
		return err
	}
	fmt.Fprintf(os.Stderr, "Open %s in your browser and enter this code:\n\n    %s\n\nApprove only if the page asks for this code and you started this login.\n", started.VerificationURI, started.UserCode)
	if !*noBrowser {
		if err := openBrowser(started.VerificationURI); err != nil {
			fmt.Fprintln(os.Stderr, "Could not open a browser; copy the URL above.")
		}
	}
	interval := time.Duration(started.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	expires := time.NewTimer(time.Duration(started.ExpiresIn) * time.Second)
	defer expires.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-expires.C:
			return errors.New("authorization request expired")
		case <-time.After(interval):
		}
		issued, err := client.PollDevice(ctx, started.DeviceCode, verifier)
		if err != nil {
			wait, err := pollOutcome(err)
			if err != nil {
				return err
			}
			interval += wait
			continue
		}
		if err := credentials.Save(*configPath, credentials.Config{
			ServerURL: client.ServerURL(), AccessToken: issued.AccessToken, Credential: issued.Credential,
		}); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Authorized %q for %d hosts with scopes %s until %s.\n", issued.Credential.Label,
			len(issued.Credential.AllowedHosts), joinScopes(issued.Credential.Scopes), issued.ExpiresAt.Local().Format(time.RFC3339))
		return nil
	}
}

// pollOutcome maps a device-token poll error to extra polling delay, or to a
// final error.
func pollOutcome(err error) (time.Duration, error) {
	var apiError *api.APIError
	if !errors.As(err, &apiError) {
		return 0, err
	}
	switch {
	case apiError.Code == "authorization_pending":
		return 0, nil
	case apiError.Code == "slow_down", apiError.Status == 429:
		return 5 * time.Second, nil
	case apiError.Code == "access_denied":
		return 0, errors.New("authorization denied")
	case apiError.Code == "expired_token":
		return 0, errors.New("authorization request expired")
	}
	return 0, err
}


func logout(args []string) error {
	flags := flag.NewFlagSet("logout", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "credential file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("logout accepts flags only")
	}
	if err := credentials.Remove(*configPath); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Local scomp MCP credential removed. Revoke the client in scomp web settings if it may have been copied.")
	return nil
}

func status(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "credential file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, err := credentials.LoadEffective(*configPath)
	if err != nil {
		return err
	}
	client, err := api.NewClient(config.ServerURL, config.AccessToken)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hosts, err := client.Hosts(ctx)
	if err != nil {
		return err
	}
	label := config.Credential.Label
	credentialID := config.Credential.ID
	if label == "" {
		label = "unknown (environment token)"
	}
	if credentialID == "" {
		credentialID = "unknown (environment token)"
	}
	fmt.Printf("server: %s\nlabel: %s\ncredential: %s\nexpires: %s\nauthorized hosts: %d\n", client.ServerURL(), label, credentialID, formatTime(config.Credential.ExpiresAt), len(hosts))
	if len(config.Credential.Scopes) > 0 {
		fmt.Printf("scopes: %s\n", joinScopes(config.Credential.Scopes))
	}
	return nil
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "credential file path")
	jsonText := flags.Bool("json-text", false, "repeat each structured result as JSON text, for MCP clients that ignore structuredContent")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve accepts flags only")
	}
	config, err := credentials.LoadEffective(*configPath)
	if err != nil {
		return err
	}
	if !config.Credential.ExpiresAt.IsZero() && !time.Now().Before(config.Credential.ExpiresAt) {
		return errors.New("credential expired; run scomp-mcp login")
	}
	client, err := api.NewClient(config.ServerURL, config.AccessToken)
	if err != nil {
		return err
	}
	canWrite := strings.TrimSpace(os.Getenv(credentials.EnvToken)) != "" || hasScope(config.Credential.Scopes, api.ScopeTerminalWrite)
	terminals := terminal.NewManager(client, canWrite)
	defer terminals.CloseAll()
	server := mcpserver.New(client, terminals, version, mcpserver.Options{JSONText: *jsonText})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx, &mcp.StdioTransport{MaxLineLength: 1 << 20})
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}

func defaultLabel() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "scomp-mcp"
	}
	return "scomp-mcp on " + hostname
}

func hasScope(scopes []api.Scope, wanted api.Scope) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func joinScopes(scopes []api.Scope) string {
	values := make([]string, len(scopes))
	for index := range scopes {
		values[index] = string(scopes[index])
	}
	return strings.Join(values, ", ")
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "unknown (environment token)"
	}
	return value.Local().Format(time.RFC3339)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: scomp-mcp <login|logout|status|serve|version> [flags]")
}

func usageError() error {
	printUsage()
	return errors.New("a valid command is required")
}

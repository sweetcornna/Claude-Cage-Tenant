// Package cagemobile exposes the native signing and foreground connection API
// to generated Java and Objective-C code. gomobile's generated module must be
// able to import this public package; the implementation stays internal.
package cagemobile

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sylvinus/agent-vm/internal/mobilelink"
	"github.com/sylvinus/agent-vm/internal/mobilelink/nativeclient"
)

func DefaultRelay() string { return mobilelink.OfficialRelay }

type DeviceSigner interface {
	PublicKeyDER() ([]byte, error)
	SignSHA256(digest []byte) ([]byte, error)
}

func Fingerprint(device DeviceSigner) (string, error) { return nativeclient.Fingerprint(device) }

type Client struct{ native *nativeclient.Client }

func Open(relayOrigin, roomID, clientToken, trustedHostPin string, device DeviceSigner) (*Client, error) {
	client, err := nativeclient.Open(relayOrigin, roomID, clientToken, trustedHostPin, device)
	if err != nil {
		return nil, err
	}
	return &Client{native: client}, nil
}

func OpenSaved(relayOrigin, lookupToken, trustedHostPin string, device DeviceSigner) (*Client, error) {
	client, err := nativeclient.OpenSaved(relayOrigin, lookupToken, trustedHostPin, device)
	if err != nil {
		return nil, err
	}
	return &Client{native: client}, nil
}

func (c *Client) StartUI(directory string) (string, error) { return c.native.StartUI(directory) }
func (c *Client) Suspend()                                 { c.native.Suspend() }
func (c *Client) Resume() bool                             { return c.native.Resume() }
func (c *Client) Close() error                             { return c.native.Close() }

// ReportDevice is optional metadata for the desktop pairing card. An older host
// can refuse the operation without preventing the phone from opening its UI.
func (c *Client) ReportDevice(platform, model string) {
	body, _ := json.Marshal(mobilelink.DeviceInfo{Platform: platform, Model: mobilelink.DeviceModel(platform, model)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.native.Do(ctx, "POST", "/api/mobile/device", body)
}

// Tenant is a signed-in account on a multi-tenant Cage server portal. The
// password stays in Go memory to sign in again after the portal session
// expires; Close signs out and forgets it.
type Tenant struct{ account *mobilelink.TenantAccount }

// TenantPortal returns the canonical HTTPS portal origin, or an error whose
// message can be shown to the user.
func TenantPortal(raw string) (string, error) { return mobilelink.CanonicalTenantPortal(raw) }

func TenantLogin(portal, username, password string) (*Tenant, error) {
	secret := []byte(password)
	defer clear(secret)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	account, err := mobilelink.LoginTenant(ctx, portal, username, secret)
	if err != nil {
		return nil, err
	}
	return &Tenant{account: account}, nil
}

func (t *Tenant) Portal() string { return t.account.Portal() }
func (t *Tenant) User() string   { return t.account.User() }

// VirtualMachines lists the account's virtual machines as a JSON array of
// {id, name, system_label, state, message}.
func (t *Tenant) VirtualMachines() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vms, err := t.account.VMs(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(vms)
	return string(data), err
}

// CreateVirtualMachine records a new virtual machine and returns it as a JSON
// object. The server prepares it the first time it is opened.
func (t *Tenant) CreateVirtualMachine(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vm, err := t.account.CreateVM(ctx, name)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(vm)
	return string(data), err
}

// OpenVirtualMachine enters a virtual machine, waiting up to fifteen minutes
// while the server starts it. Call StartUI on the result as for a paired computer.
func (t *Tenant) OpenVirtualMachine(vmID string) (*Client, error) {
	client, err := nativeclient.OpenTenant(t.account, vmID, 15*time.Minute)
	if err != nil {
		return nil, err
	}
	return &Client{native: client}, nil
}

func (t *Tenant) Close() { t.account.Close() }

func Pair(code, relay string, device DeviceSigner) (string, error) {
	pin, err := Fingerprint(device)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return mobilelink.RedeemPair(ctx, code, relay, pin)
}

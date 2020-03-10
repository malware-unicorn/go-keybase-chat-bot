package kbchat

import (

	"encoding/json"
	"errors"
	"fmt"
	"io"

	"log"
	"os/exec"
	"os"

	"sync"
	"time"
  "github.com/malware-unicorn/keybase-bot-api/kbapi"
	"github.com/malware-unicorn/go-keybase-chat-bot/kbchat/types/chat1"
	"github.com/malware-unicorn/go-keybase-chat-bot/kbchat/types/stellar1"
)

// API is the main object used for communicating with the Keybase JSON API
type API struct {
	sync.Mutex
	apiInput      io.Writer
	apiOutput     io.Reader
	kb            *kbapi.Kbapi
	apiCmd        *exec.Cmd
	username      string
	runOpts       RunOptions
	subscriptions []*NewSubscription
        pipeW         *os.File
        pipeR         *os.File
}

type OneshotOptions struct {
	Username string
	PaperKey string
}

type RunOptions struct {
	KeybaseLocation string
	HomeDir         string
	Oneshot         *OneshotOptions
	StartService    bool
	// Have the bot send/receive typing notifications
	EnableTyping bool
	// Disable bot lite mode
	DisableBotLiteMode bool
}

func (r RunOptions) Location() string {
	if r.KeybaseLocation == "" {
		return "keybase"
	}
	return r.KeybaseLocation
}

func (r RunOptions) Command(args ...string) *exec.Cmd {
	var cmd []string
	if r.HomeDir != "" {
		cmd = append(cmd, "--home", r.HomeDir)
	}
	cmd = append(cmd, args...)
	return exec.Command(r.Location(), cmd...)
}

// Start fires up the Keybase JSON API in stdin/stdout mode
func Start(runOpts RunOptions) (*API, error) {
	api := &API{
		runOpts: runOpts,
		kb: kbapi.NewKbApi(),
	}
	if err := api.startPipes(); err != nil {
                api.pipeW.Close()
                api.pipeR.Close()
		return nil, err
	}
	return api, nil
}

func (a *API) Command(args ...string) *exec.Cmd {
	return a.runOpts.Command(args...)
}

func (a *API) auth() (string, error) {
	username := a.kb.GetUsername()
	if username != "" {
		return username, nil
	}
	if a.runOpts.Oneshot == nil {
		return "", errors.New("Oneshot is nil")
	}
	username = ""
	// If a paper key is specified, then login with oneshot mode (logout first)
	if a.runOpts.Oneshot != nil {
		if username == a.runOpts.Oneshot.Username {
			// just get out if we are on the desired user already
			return username, nil
		}
		if err:= kbapi.Logout(a.kb.GetGlobalContext(), true); err != nil {
			return "", err
		}
		if err:= kbapi.LoginOneshot(a.kb.GetGlobalContext(), a.runOpts.Oneshot.Username, a.runOpts.Oneshot.PaperKey); err != nil {
			return "", err
		}
		username = a.runOpts.Oneshot.Username
		return username, nil
	}
	return "", errors.New("unable to auth")
}

func (a *API) startPipes() (err error) {
	a.Lock()
	defer a.Unlock()
	a.apiCmd = nil

	if a.runOpts.StartService {
		args := []string{fmt.Sprintf("-enable-bot-lite-mode=%v", a.runOpts.DisableBotLiteMode), "service"}
		if err := a.runOpts.Command(args...).Start(); err != nil {
			return err
		}
	}

	if a.username, err = a.auth(); err != nil {
		return err
	}
	// Disable Typing
  if err := kbapi.SetChatSettings(a.kb.GetGlobalContext()); err != nil {
		return err
	}

	pipeR, pipeW, _ := os.Pipe()
	a.pipeW = pipeW
	a.pipeR = pipeR

	return nil
}

var errAPIDisconnected = errors.New("chat API disconnected")

func (a *API) getAPIPipesLocked() (io.Writer, io.Reader, error) {
	// this should only be called inside a lock
	if a.apiCmd == nil {
		return nil, nil, errAPIDisconnected
	}
	return a.apiInput, a.apiOutput, nil
}

func (a *API) GetUsername() string {
	return a.username
}

func (a *API) doSend(arg interface{}) (resp SendResponse, err error) {
	a.Lock()
	defer a.Unlock()

	bArg, err := json.Marshal(arg)
	if err != nil {
		return SendResponse{}, err
	}
	responseRaw, err := a.kb.SendChatApi(string(bArg))
	if err != nil {
		return SendResponse{}, err
	}
	if err := json.Unmarshal(responseRaw, &resp); err != nil {
		return resp, fmt.Errorf("failed to decode API response: %s", err)
	} else if resp.Error != nil {
		return resp, errors.New(resp.Error.Message)
	}
	return resp, nil
}

func (a *API) doFetch(apiInput string) ([]byte, error) {
	a.Lock()
	defer a.Unlock()

	byteOutput, err := a.kb.SendChatApi(apiInput)
	if err != nil {
		return nil, err
	}
	return byteOutput, nil
}

// SubscriptionMessage contains a message and conversation object
type SubscriptionMessage struct {
	Message      chat1.MsgSummary
	Conversation chat1.ConvSummary
}

type SubscriptionConversation struct {
	Conversation chat1.ConvSummary
}

type SubscriptionWalletEvent struct {
	Payment stellar1.PaymentDetailsLocal
}

// NewSubscription has methods to control the background message fetcher loop
type NewSubscription struct {
	sync.Mutex

	newMsgsCh   <-chan SubscriptionMessage
	newConvsCh  <-chan SubscriptionConversation
	newWalletCh <-chan SubscriptionWalletEvent
	errorCh     <-chan error
	running     bool
	shutdownCh  chan struct{}
}

// Read blocks until a new message arrives
func (m *NewSubscription) Read() (SubscriptionMessage, error) {
	select {
	case msg := <-m.newMsgsCh:
		return msg, nil
	case err := <-m.errorCh:
		return SubscriptionMessage{}, err
	case <-m.shutdownCh:
		return SubscriptionMessage{}, errors.New("Subscription shutdown")
	}
}

func (m *NewSubscription) ReadNewConvs() (SubscriptionConversation, error) {
	select {
	case conv := <-m.newConvsCh:
		return conv, nil
	case err := <-m.errorCh:
		return SubscriptionConversation{}, err
	case <-m.shutdownCh:
		return SubscriptionConversation{}, errors.New("Subscription shutdown")
	}
}

// Read blocks until a new message arrives
func (m *NewSubscription) ReadWallet() (SubscriptionWalletEvent, error) {
	select {
	case msg := <-m.newWalletCh:
		return msg, nil
	case err := <-m.errorCh:
		return SubscriptionWalletEvent{}, err
	case <-m.shutdownCh:
		return SubscriptionWalletEvent{}, errors.New("Subscription shutdown")
	}
}

// Shutdown terminates the background process
func (m *NewSubscription) Shutdown() {
	m.Lock()
	defer m.Unlock()
	if m.running {
		close(m.shutdownCh)
		m.running = false
	}
}

type ListenOptions struct {
	Wallet bool
	Convs  bool
}

type PaymentHolder struct {
	Payment stellar1.PaymentDetailsLocal `json:"notification"`
}

type TypeHolder struct {
	Type string `json:"type"`
}

// ListenForNewTextMessages proxies to Listen without wallet events
func (a *API) ListenForNewTextMessages() (*NewSubscription, error) {
	opts := ListenOptions{Wallet: false}
	return a.Listen(opts)
}

func (a *API) registerSubscription(sub *NewSubscription) {
	a.Lock()
	defer a.Unlock()
	a.subscriptions = append(a.subscriptions, sub)
}

// Listen fires of a background loop and puts chat messages and wallet
// events into channels
func (a *API) Listen(opts ListenOptions) (*NewSubscription, error) {
	newMsgsCh := make(chan SubscriptionMessage, 100)
	newConvsCh := make(chan SubscriptionConversation, 100)
	newWalletCh := make(chan SubscriptionWalletEvent, 100)
	errorCh := make(chan error, 100)
	shutdownCh := make(chan struct{})
	done := make(chan struct{})

	sub := &NewSubscription{
		newMsgsCh:   newMsgsCh,
		newConvsCh:  newConvsCh,
		newWalletCh: newWalletCh,
		shutdownCh:  shutdownCh,
		errorCh:     errorCh,
		running:     true,
	}
	a.registerSubscription(sub)
	pause := 2 * time.Second
	readScanner := func() {
		for {
			buff, err := a.kb.ReadListener(a.pipeR)
			if err != nil {
				errorCh <- err
				break
			}
			var typeHolder TypeHolder
			if err := json.Unmarshal(buff, &typeHolder); err != nil {
				errorCh <- err
				break
			}
			switch typeHolder.Type {
			case "chat":
				var notification chat1.MsgNotification
				if err := json.Unmarshal(buff, &notification); err != nil {
					errorCh <- err
					break
				}
				if notification.Error != nil {
					log.Printf("error message received: %s", *notification.Error)
				} else if notification.Msg != nil {
					subscriptionMessage := SubscriptionMessage{
						Message: *notification.Msg,
						Conversation: chat1.ConvSummary{
							Id:      notification.Msg.ConvID,
							Channel: notification.Msg.Channel,
						},
					}
					newMsgsCh <- subscriptionMessage
				}
			case "chat_conv":
				var notification chat1.ConvNotification
				if err := json.Unmarshal(buff, &notification); err != nil {
					errorCh <- err
					break
				}
				if notification.Error != nil {
					log.Printf("error message received: %s", *notification.Error)
				} else if notification.Conv != nil {
					subscriptionConv := SubscriptionConversation{
						Conversation: *notification.Conv,
					}
					newConvsCh <- subscriptionConv
				}
			case "wallet":
				var holder PaymentHolder
				if err := json.Unmarshal(buff, &holder); err != nil {
					errorCh <- err
					break
				}
				subscriptionPayment := SubscriptionWalletEvent(holder)
				newWalletCh <- subscriptionPayment
			default:
				continue
			}
		}
		done <- struct{}{}
	}

	attempts := 0
	maxAttempts := 1800

	go func() {
		for {
			select {
			case <-shutdownCh:
				log.Printf("Listen: received shutdown")
				return
			default:
			}

			if attempts >= maxAttempts {
				panic("Listen: failed to auth, giving up")
			}
			attempts++

			if err := a.kb.StartChatApiListener(a.pipeW); err != nil {
				log.Printf("Listen: failed to make listen scanner: %s", err)
			  time.Sleep(pause)
			 	continue
			}
			attempts = 0

			<-done
			time.Sleep(pause)
		}
	}()
	go readScanner()

	return sub, nil
}

func (a *API) LogSend(feedback string) error {
	feedback = "go-keybase-chat-bot log send\n" +
		"username: " + a.GetUsername() + "\n" +
		feedback

	args := []string{
		"log", "send",
		"--no-confirm",
		"--feedback", feedback,
	}

	// We're determining whether the service is already running by running status
	// with autofork disabled.
	if err := a.runOpts.Command("--no-auto-fork", "status"); err != nil {
		// Assume that there's no service running, so log send as standalone
		args = append([]string{"--standalone"}, args...)
	}

	return a.runOpts.Command(args...).Run()
}

func (a *API) Shutdown() error {
	a.Lock()
	defer a.Unlock()
        a.pipeW.Close()
        a.pipeR.Close()
	for _, sub := range a.subscriptions {
		sub.Shutdown()
	}

	if a.runOpts.Oneshot != nil {
		if err:= kbapi.Logout(a.kb.GetGlobalContext(), true); err != nil {
			return err
		}
	}

	if a.runOpts.StartService {
		err := kbapi.CtlStop(a.kb.GetGlobalContext(), true)
		if err != nil {
			return err
		}
	}

	return nil
}

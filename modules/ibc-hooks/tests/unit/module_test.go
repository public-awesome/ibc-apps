package tests_unit

import (
	"encoding/json"
	"fmt"
	"testing"

	ibc_hooks "github.com/cosmos/ibc-apps/modules/ibc-hooks/v8"
	ibchookskeeper "github.com/cosmos/ibc-apps/modules/ibc-hooks/v8/keeper"
	"github.com/cosmos/ibc-apps/modules/ibc-hooks/v8/simapp"
	"github.com/cosmos/ibc-apps/modules/ibc-hooks/v8/tests/unit/mocks"
	"github.com/stretchr/testify/suite"

	_ "embed"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/types"

	capabilitytypes "github.com/cosmos/ibc-go/modules/capability/types"
	ibctransfer "github.com/cosmos/ibc-go/v8/modules/apps/transfer"
	transfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	ibcclienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	ibcmock "github.com/cosmos/ibc-go/v8/testing/mock"
)

//go:embed testdata/counter/artifacts/counter.wasm
var counterWasm []byte

//go:embed testdata/echo/artifacts/echo.wasm
var echoWasm []byte

type HooksTestSuite struct {
	suite.Suite

	App                 *simapp.App
	Ctx                 sdk.Context
	EchoContractAddr    sdk.AccAddress
	CounterContractAddr sdk.AccAddress
	TestAddress         *types.BaseAccount
}

type mockPauseChecker struct {
	paused map[string]bool
}

func (m mockPauseChecker) IsExecutionPaused(_ sdk.Context, contractAddr sdk.AccAddress) bool {
	return m.paused[contractAddr.String()]
}

func TestIBCHooksTestSuite(t *testing.T) {
	suite.Run(t, new(HooksTestSuite))
}

func (suite *HooksTestSuite) SetupEnv() {
	// Setup the environment
	app, ctx, acc := simapp.Setup(suite.T())

	// create the echo contract
	contractID, _, err := app.ContractKeeper.Create(ctx, acc.GetAddress(), counterWasm, nil)
	suite.NoError(err)
	counterContractAddr, _, err := app.ContractKeeper.Instantiate(
		ctx,
		contractID,
		acc.GetAddress(),
		nil,
		[]byte(`{"count": 0}`),
		"counter contract",
		nil,
	)
	suite.NoError(err)
	suite.Equal("cosmos14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9s4hmalr", counterContractAddr.String())

	// create the counter contract
	contractID, _, err = app.ContractKeeper.Create(ctx, acc.GetAddress(), echoWasm, nil)
	suite.NoError(err)
	echoContractAddr, _, err := app.ContractKeeper.Instantiate(
		ctx,
		contractID,
		acc.GetAddress(),
		nil,
		[]byte(`{}`),
		"echo contract",
		nil,
	)
	suite.NoError(err)
	suite.Equal("cosmos1nc5tatafv6eyq7llkr2gv50ff9e22mnf70qgjlv737ktmt4eswrqez7la9", echoContractAddr.String())

	suite.App = app
	suite.Ctx = ctx
	suite.EchoContractAddr = echoContractAddr
	suite.CounterContractAddr = counterContractAddr
	suite.TestAddress = acc
}

func (suite *HooksTestSuite) TestOnRecvPacketEcho() {
	// create en env
	suite.SetupEnv()

	// Create the packet
	recvPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.EchoContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"echo":{"msg":"test"}}}}`, suite.EchoContractAddr.String()),
		}.GetBytes(),
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}

	// send funds to the escrow address to simulate a transfer from the ibc module
	escrowAddress := transfertypes.GetEscrowAddress(recvPacket.GetDestPort(), recvPacket.GetDestChannel())
	testEscrowAmount := sdk.NewInt64Coin("stake", 2)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, suite.TestAddress.GetAddress(), escrowAddress, sdk.NewCoins(testEscrowAmount))
	suite.NoError(err)

	// since ibc-go >= 7.1.0 escrow needs to be explicitly tracked
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, testEscrowAmount)
	}

	// create the wasm hooks
	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)

	// create the ics4 middleware
	ics4Middleware := ibc_hooks.NewICS4Middleware(
		suite.App.IBCKeeper.ChannelKeeper,
		wasmHooks,
	)

	// create the ibc middleware
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(
		transferIBCModule,
		&ics4Middleware,
	)

	// call the hook twice
	res := ibcmiddleware.OnRecvPacket(
		suite.Ctx,
		recvPacket,
		suite.TestAddress.GetAddress(),
	)
	suite.True(res.Success())
	var ack map[string]string // This can't be unmarshalled to Acknowledgement because it's fetched from the events
	err = json.Unmarshal(res.Acknowledgement(), &ack)
	suite.Require().NoError(err)
	suite.Require().NotContains(ack, "error")
	suite.Require().Equal(ack["result"], "eyJjb250cmFjdF9yZXN1bHQiOiJkR2hwY3lCemFHOTFiR1FnWldOb2J3PT0iLCJpYmNfYWNrIjoiZXlKeVpYTjFiSFFpT2lKQlVUMDlJbjA9In0=")
}

func (suite *HooksTestSuite) TestOnRecvPacketCounterContract() {
	// create en env
	suite.SetupEnv()

	// Create the packet
	recvPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"increment":{}}}}`, suite.CounterContractAddr.String()),
		}.GetBytes(),
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}

	// send funds to the escrow address to simulate a transfer from the ibc module
	escrowAddress := transfertypes.GetEscrowAddress(recvPacket.GetDestPort(), recvPacket.GetDestChannel())
	testEscrowAmount := sdk.NewInt64Coin("stake", 2)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, suite.TestAddress.GetAddress(), escrowAddress, sdk.NewCoins(testEscrowAmount))
	suite.NoError(err)

	// since ibc-go >= 7.1.0 escrow needs to be explicitly tracked
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, testEscrowAmount)
	}

	// create the wasm hooks
	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)

	// create the ics4 middleware
	ics4Middleware := ibc_hooks.NewICS4Middleware(
		suite.App.IBCKeeper.ChannelKeeper,
		wasmHooks,
	)

	// create the ibc middleware
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(
		transferIBCModule,
		&ics4Middleware,
	)

	// call the hook twice
	res := ibcmiddleware.OnRecvPacket(
		suite.Ctx,
		recvPacket,
		suite.TestAddress.GetAddress(),
	)
	suite.True(res.Success())
	res = ibcmiddleware.OnRecvPacket(
		suite.Ctx,
		recvPacket,
		suite.TestAddress.GetAddress(),
	)
	suite.True(res.Success())

	// get the derived account to check the count
	senderBech32, err := ibchookskeeper.DeriveIntermediateSender(
		recvPacket.GetDestChannel(),
		suite.TestAddress.GetAddress().String(),
		"cosmos",
	)
	suite.NoError(err)
	// query the smart contract to assert the count
	count, err := suite.App.WasmKeeper.QuerySmart(
		suite.Ctx,
		suite.CounterContractAddr,
		[]byte(fmt.Sprintf(`{"get_count":{"addr": "%s"}}`, senderBech32)),
	)
	suite.NoError(err)
	suite.Equal(`{"count":1}`, string(count))
}

func (suite *HooksTestSuite) TestOnRecvPacketPausedContractRejected() {
	suite.SetupEnv()

	recvPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"increment":{}}}}`, suite.CounterContractAddr.String()),
		}.GetBytes(),
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}

	escrowAddress := transfertypes.GetEscrowAddress(recvPacket.GetDestPort(), recvPacket.GetDestChannel())
	testEscrowAmount := sdk.NewInt64Coin("stake", 2)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, suite.TestAddress.GetAddress(), escrowAddress, sdk.NewCoins(testEscrowAmount))
	suite.NoError(err)

	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, testEscrowAmount)
	}

	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)
	wasmHooks.SetPauseChecker(mockPauseChecker{
		paused: map[string]bool{suite.CounterContractAddr.String(): true},
	})

	ics4Middleware := ibc_hooks.NewICS4Middleware(
		suite.App.IBCKeeper.ChannelKeeper,
		wasmHooks,
	)
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(
		transferIBCModule,
		&ics4Middleware,
	)

	res := ibcmiddleware.OnRecvPacket(suite.Ctx, recvPacket, suite.TestAddress.GetAddress())
	suite.False(res.Success())

	// Error ack is what triggers refund handling on sender side.
	var ack map[string]any
	err = json.Unmarshal(res.Acknowledgement(), &ack)
	suite.NoError(err)
	_, hasError := ack["error"]
	suite.True(hasError)
}

func (suite *HooksTestSuite) TestOnRecvPacketPausedContractSenderSideRefund() {
	suite.SetupEnv()

	sender := suite.TestAddress.GetAddress()
	sendAmount := sdk.NewInt64Coin("stake", 1)

	// Record sender/escrow initial state.
	initialBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, sender, "stake")
	escrowAddress := transfertypes.GetEscrowAddress("transfer", "channel-0")
	initialEscrowBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, escrowAddress, "stake")
	initialTrackedEscrow := sdk.NewInt64Coin("stake", 0)

	// --- Sender side: simulate sending tokens via IBC transfer ---
	// Escrow the tokens (what the transfer module does during SendTransfer)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, sender, escrowAddress, sdk.NewCoins(sendAmount))
	suite.NoError(err)
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		initialTrackedEscrow = transferKeeper.GetTotalEscrowForDenom(suite.Ctx, "stake")
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, initialTrackedEscrow.Add(sendAmount))
	}

	// Verify sender balance decreased and escrow increased after send.
	afterSendBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, sender, "stake")
	suite.Equal(initialBalance.Sub(sendAmount), afterSendBalance)
	afterSendEscrowBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, escrowAddress, "stake")
	suite.Equal(initialEscrowBalance.Add(sendAmount), afterSendEscrowBalance)

	// Build the packet from the sender chain's perspective (native "stake" denom)
	packet := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "stake",
			Amount:   "1",
			Sender:   sender.String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"increment":{}}}}`, suite.CounterContractAddr.String()),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
		DestinationPort:    "transfer",
		DestinationChannel: "channel-0",
	}

	// Build middleware with a paused-contract checker and get the ack from OnRecvPacket.
	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)
	wasmHooks.SetPauseChecker(mockPauseChecker{
		paused: map[string]bool{suite.CounterContractAddr.String(): true},
	})
	ics4Middleware := ibc_hooks.NewICS4Middleware(
		suite.App.IBCKeeper.ChannelKeeper,
		wasmHooks,
	)
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(
		transferIBCModule,
		&ics4Middleware,
	)

	recvRes := ibcmiddleware.OnRecvPacket(suite.Ctx, packet, sender)
	suite.False(recvRes.Success())

	var ack map[string]any
	err = json.Unmarshal(recvRes.Acknowledgement(), &ack)
	suite.NoError(err)
	_, hasError := ack["error"]
	suite.True(hasError)

	// --- Sender side: process the paused-contract error ack ---
	err = ibcmiddleware.OnAcknowledgementPacket(suite.Ctx, packet, recvRes.Acknowledgement(), sender)
	suite.NoError(err)

	// Verify sender got refunded and escrow/tracking returned to initial values.
	finalBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, sender, "stake")
	suite.Equal(initialBalance, finalBalance)
	finalEscrowBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, escrowAddress, "stake")
	suite.Equal(initialEscrowBalance, finalEscrowBalance)
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		suite.Equal(initialTrackedEscrow, transferKeeper.GetTotalEscrowForDenom(suite.Ctx, "stake"))
	}
}

func (suite *HooksTestSuite) TestOnRecvPacketUnpausedContractSenderSideNoRefund() {
	suite.SetupEnv()

	sender := suite.TestAddress.GetAddress()
	sendAmount := sdk.NewInt64Coin("stake", 1)

	// Record sender/escrow initial state.
	initialBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, sender, "stake")
	escrowAddress := transfertypes.GetEscrowAddress("transfer", "channel-0")
	initialEscrowBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, escrowAddress, "stake")
	initialTrackedEscrow := sdk.NewInt64Coin("stake", 0)

	// --- Sender side: simulate sending tokens via IBC transfer ---
	// Escrow the tokens (what the transfer module does during SendTransfer)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, sender, escrowAddress, sdk.NewCoins(sendAmount))
	suite.NoError(err)
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		initialTrackedEscrow = transferKeeper.GetTotalEscrowForDenom(suite.Ctx, "stake")
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, initialTrackedEscrow.Add(sendAmount))
	}

	// Verify sender balance decreased and escrow increased after send.
	afterSendBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, sender, "stake")
	suite.Equal(initialBalance.Sub(sendAmount), afterSendBalance)
	afterSendEscrowBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, escrowAddress, "stake")
	suite.Equal(initialEscrowBalance.Add(sendAmount), afterSendEscrowBalance)

	// Build the packet from the sender chain's perspective (native "stake" denom)
	packet := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "stake",
			Amount:   "1",
			Sender:   sender.String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"increment":{}}}}`, suite.CounterContractAddr.String()),
		}.GetBytes(),
		Sequence:           1,
		SourcePort:         "transfer",
		SourceChannel:      "channel-0",
		DestinationPort:    "transfer",
		DestinationChannel: "channel-0",
	}

	// Build middleware with an explicitly unpaused checker and get success ack from OnRecvPacket.
	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)
	wasmHooks.SetPauseChecker(mockPauseChecker{
		paused: map[string]bool{suite.CounterContractAddr.String(): false},
	})
	ics4Middleware := ibc_hooks.NewICS4Middleware(
		suite.App.IBCKeeper.ChannelKeeper,
		wasmHooks,
	)
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(
		transferIBCModule,
		&ics4Middleware,
	)

	recvRes := ibcmiddleware.OnRecvPacket(suite.Ctx, packet, sender)
	suite.True(recvRes.Success())

	var ack map[string]any
	err = json.Unmarshal(recvRes.Acknowledgement(), &ack)
	suite.NoError(err)
	_, hasError := ack["error"]
	suite.False(hasError)
	_, hasResult := ack["result"]
	suite.True(hasResult)

	// --- Sender side: process success ack ---
	err = ibcmiddleware.OnAcknowledgementPacket(suite.Ctx, packet, recvRes.Acknowledgement(), sender)
	suite.NoError(err)

	// Verify sender was not refunded and escrow/tracking stay in sent state.
	finalBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, sender, "stake")
	suite.Equal(afterSendBalance, finalBalance)
	finalEscrowBalance := suite.App.BankKeeper.GetBalance(suite.Ctx, escrowAddress, "stake")
	suite.Equal(afterSendEscrowBalance, finalEscrowBalance)
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		suite.Equal(initialTrackedEscrow.Add(sendAmount), transferKeeper.GetTotalEscrowForDenom(suite.Ctx, "stake"))
	}
}

func (suite *HooksTestSuite) TestOnAcknowledgementPacketCounterContract() {
	suite.SetupEnv()

	callbackPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"ibc_callback": "%s"}`, suite.CounterContractAddr),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}

	// send funds to the escrow address to simulate a transfer from the ibc module
	escrowAddress := transfertypes.GetEscrowAddress(callbackPacket.GetDestPort(), callbackPacket.GetDestChannel())
	testEscrowAmount := sdk.NewInt64Coin("stake", 2)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, suite.TestAddress.GetAddress(), escrowAddress, sdk.NewCoins(testEscrowAmount))
	suite.NoError(err)

	// since ibc-go >= 7.1.0 escrow needs to be explicitly tracked
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, testEscrowAmount)
	}

	// create the wasm hooks
	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)

	// create the ics4 middleware
	ics4Middleware := ibc_hooks.NewICS4Middleware(
		&mocks.ICS4WrapperMock{},
		wasmHooks,
	)

	// create the ibc middleware
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(
		transferIBCModule,
		&ics4Middleware,
	)

	// call the hook
	seq, err := ibcmiddleware.SendPacket(
		suite.Ctx,
		&capabilitytypes.Capability{Index: 1},
		callbackPacket.SourcePort,
		callbackPacket.SourceChannel,
		ibcclienttypes.Height{
			RevisionNumber: 1,
			RevisionHeight: 1,
		},
		1,
		callbackPacket.Data,
	)

	// require to be the first sequence
	suite.Equal(uint64(1), seq)
	// assert the request was successful
	suite.NoError(err)

	// Create the packet
	recvPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"increment":{}}}}`, suite.CounterContractAddr.String()),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}
	suite.NoError(err)
	err = wasmHooks.OnAcknowledgementPacketOverride(
		ibcmiddleware,
		suite.Ctx,
		recvPacket,
		ibcmock.MockAcknowledgement.Acknowledgement(),
		suite.TestAddress.GetAddress(),
	)
	// assert the request was successful
	suite.NoError(err)

	// query the smart contract to assert the count
	count, err := suite.App.WasmKeeper.QuerySmart(
		suite.Ctx,
		suite.CounterContractAddr,
		[]byte(fmt.Sprintf(`{"get_count":{"addr": %q}}`, suite.CounterContractAddr.String())),
	)
	suite.NoError(err)
	suite.Equal(`{"count":1}`, string(count))
}

func (suite *HooksTestSuite) TestOnTimeoutPacketOverrideCounterContract() {
	suite.SetupEnv()
	callbackPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"ibc_callback": "%s"}`, suite.CounterContractAddr),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}

	// send funds to the escrow address to simulate a transfer from the ibc module
	escrowAddress := transfertypes.GetEscrowAddress(callbackPacket.GetDestPort(), callbackPacket.GetDestChannel())
	testEscrowAmount := sdk.NewInt64Coin("stake", 2)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, suite.TestAddress.GetAddress(), escrowAddress, sdk.NewCoins(testEscrowAmount))
	suite.NoError(err)

	// since ibc-go >= 7.1.0 escrow needs to be explicitly tracked
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, testEscrowAmount)
	}

	// create the wasm hooks
	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)

	// create the ics4 middleware
	ics4Middleware := ibc_hooks.NewICS4Middleware(
		&mocks.ICS4WrapperMock{},
		wasmHooks,
	)

	// create the ibc middleware
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(
		transferIBCModule,
		&ics4Middleware,
	)

	// call the hook
	seq, err := ibcmiddleware.SendPacket(
		suite.Ctx,
		&capabilitytypes.Capability{Index: 1},
		callbackPacket.SourcePort,
		callbackPacket.SourceChannel,
		ibcclienttypes.Height{
			RevisionNumber: 1,
			RevisionHeight: 1,
		},
		1,
		callbackPacket.Data,
	)

	// require to be the first sequence
	suite.Equal(uint64(1), seq)
	// assert the request was successful
	suite.NoError(err)

	// Create the packet
	recvPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"increment":{}}}}`, suite.CounterContractAddr.String()),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}
	suite.NoError(err)
	err = wasmHooks.OnTimeoutPacketOverride(
		ibcmiddleware,
		suite.Ctx,
		recvPacket,
		suite.TestAddress.GetAddress(),
	)
	// assert the request was successful
	suite.NoError(err)

	// query the smart contract to assert the count
	count, err := suite.App.WasmKeeper.QuerySmart(
		suite.Ctx,
		suite.CounterContractAddr,
		[]byte(fmt.Sprintf(`{"get_count":{"addr": %q}}`, suite.CounterContractAddr.String())),
	)
	suite.NoError(err)
	suite.Equal(`{"count":10}`, string(count))
}

func (suite *HooksTestSuite) TestOnAcknowledgementPacketPausedContractCallbackRejectedAndRetained() {
	suite.SetupEnv()

	callbackPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"ibc_callback": "%s"}`, suite.CounterContractAddr),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}

	escrowAddress := transfertypes.GetEscrowAddress(callbackPacket.GetDestPort(), callbackPacket.GetDestChannel())
	testEscrowAmount := sdk.NewInt64Coin("stake", 2)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, suite.TestAddress.GetAddress(), escrowAddress, sdk.NewCoins(testEscrowAmount))
	suite.NoError(err)
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, testEscrowAmount)
	}

	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)
	wasmHooks.SetPauseChecker(mockPauseChecker{
		paused: map[string]bool{suite.CounterContractAddr.String(): true},
	})

	ics4Middleware := ibc_hooks.NewICS4Middleware(
		&mocks.ICS4WrapperMock{},
		wasmHooks,
	)
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(transferIBCModule, &ics4Middleware)

	seq, err := ibcmiddleware.SendPacket(
		suite.Ctx,
		&capabilitytypes.Capability{Index: 1},
		callbackPacket.SourcePort,
		callbackPacket.SourceChannel,
		ibcclienttypes.Height{RevisionNumber: 1, RevisionHeight: 1},
		1,
		callbackPacket.Data,
	)
	suite.NoError(err)
	suite.Equal(uint64(1), seq)

	recvPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"increment":{}}}}`, suite.CounterContractAddr.String()),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}
	err = wasmHooks.OnAcknowledgementPacketOverride(
		ibcmiddleware,
		suite.Ctx,
		recvPacket,
		ibcmock.MockAcknowledgement.Acknowledgement(),
		suite.TestAddress.GetAddress(),
	)
	suite.Error(err)
	suite.Contains(err.Error(), "contract is paused")

	stored := suite.App.IBCHooksKeeper.GetPacketCallback(suite.Ctx, recvPacket.GetSourceChannel(), recvPacket.GetSequence())
	suite.Equal(suite.CounterContractAddr.String(), stored)
}

func (suite *HooksTestSuite) TestOnTimeoutPacketPausedContractCallbackRejectedAndRetained() {
	suite.SetupEnv()

	callbackPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"ibc_callback": "%s"}`, suite.CounterContractAddr),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}

	escrowAddress := transfertypes.GetEscrowAddress(callbackPacket.GetDestPort(), callbackPacket.GetDestChannel())
	testEscrowAmount := sdk.NewInt64Coin("stake", 2)
	err := suite.App.BankKeeper.SendCoins(suite.Ctx, suite.TestAddress.GetAddress(), escrowAddress, sdk.NewCoins(testEscrowAmount))
	suite.NoError(err)
	if transferKeeper, ok := any(&suite.App.TransferKeeper).(TransferKeeperWithTotalEscrowTracking); ok {
		transferKeeper.SetTotalEscrowForDenom(suite.Ctx, testEscrowAmount)
	}

	wasmHooks := ibc_hooks.NewWasmHooks(
		&suite.App.IBCHooksKeeper,
		&suite.App.WasmKeeper,
		"cosmos",
	)
	wasmHooks.SetPauseChecker(mockPauseChecker{
		paused: map[string]bool{suite.CounterContractAddr.String(): true},
	})

	ics4Middleware := ibc_hooks.NewICS4Middleware(
		&mocks.ICS4WrapperMock{},
		wasmHooks,
	)
	transferIBCModule := ibctransfer.NewIBCModule(suite.App.TransferKeeper)
	ibcmiddleware := ibc_hooks.NewIBCMiddleware(transferIBCModule, &ics4Middleware)

	seq, err := ibcmiddleware.SendPacket(
		suite.Ctx,
		&capabilitytypes.Capability{Index: 1},
		callbackPacket.SourcePort,
		callbackPacket.SourceChannel,
		ibcclienttypes.Height{RevisionNumber: 1, RevisionHeight: 1},
		1,
		callbackPacket.Data,
	)
	suite.NoError(err)
	suite.Equal(uint64(1), seq)

	recvPacket := channeltypes.Packet{
		Data: transfertypes.FungibleTokenPacketData{
			Denom:    "transfer/channel-0/stake",
			Amount:   "1",
			Sender:   suite.TestAddress.GetAddress().String(),
			Receiver: suite.CounterContractAddr.String(),
			Memo:     fmt.Sprintf(`{"wasm":{"contract": "%s", "msg":{"increment":{}}}}`, suite.CounterContractAddr.String()),
		}.GetBytes(),
		Sequence:      1,
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
	}
	err = wasmHooks.OnTimeoutPacketOverride(
		ibcmiddleware,
		suite.Ctx,
		recvPacket,
		suite.TestAddress.GetAddress(),
	)
	suite.Error(err)
	suite.Contains(err.Error(), "contract is paused")

	stored := suite.App.IBCHooksKeeper.GetPacketCallback(suite.Ctx, recvPacket.GetSourceChannel(), recvPacket.GetSequence())
	suite.Equal(suite.CounterContractAddr.String(), stored)
}

// TransferKeeperWithTotalEscrowTracking defines an interface to check for existing methods
// in TransferKeeper.
type TransferKeeperWithTotalEscrowTracking interface {
	SetTotalEscrowForDenom(ctx sdk.Context, coin sdk.Coin)
	GetTotalEscrowForDenom(ctx sdk.Context, denom string) sdk.Coin
}

package eth

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
)

var (
	txMonitorTargetSwap = common.HexToAddress("0x013bb8a204499523ddF717e0aBAA14E6dC849060")
	txMonitorTargetBuy  = common.HexToAddress("0x5c952063c7fc8610FFDB798152D69F0B9550762b")
	txMonitorEventTopic = common.HexToHash("0x396d5e902b675b032348d3d2e9517ee8f0c4a926603fbc075d3d282ff00cad20")

	txMonitorSwapSelector = []byte{0x3d, 0x0e, 0x3e, 0xc5}
	txMonitorBuySelector  = []byte{0x87, 0xf2, 0x76, 0x55}
	txMonitorRouteSwapSel = []byte{0x4d, 0x81, 0x9a, 0x2a}

	txMonitorMinValue = big.NewInt(100000000000000000) // 0.1 BNB

	txMonitorFileOnce sync.Once
	txMonitorFile     *os.File
	txMonitorFileErr  error
	txMonitorFileMu   sync.Mutex
)

const txMonitorLogPath = "tx_monitor.log"

type txMonitorLogEntry struct {
	AcquiredAt string `json:"acquired_at"`
	Stage      string `json:"stage"`
	Source     string `json:"source"`
	TxHash     string `json:"tx_hash"`
	BlockNum   uint64 `json:"block_number,omitempty"`
	To         string `json:"to"`
	Method     string `json:"method"`
	Token      string `json:"token"`
	ValueWei   string `json:"value_wei"`
}

func logAcceptedTransactions(source string, txs []*types.Transaction, errs []error) {
	for i, tx := range txs {
		if i < len(errs) && errs[i] != nil {
			continue
		}
		logTransactionMonitor("accepted", source, tx)
	}
}

func logAcceptedTransaction(source string, tx *types.Transaction) {
	logTransactionMonitor("accepted", source, tx)
}

func logConfirmedLogs(logs []*types.Log) {
	for _, entry := range logs {
		logConfirmedLog(entry)
	}
}

func logConfirmedLog(entry *types.Log) {
	if entry == nil || entry.Removed {
		return
	}
	if entry.Address != txMonitorTargetBuy || len(entry.Topics) == 0 || entry.Topics[0] != txMonitorEventTopic {
		return
	}
	token, ok := parseConfirmedLogToken(entry.Data)
	if !ok {
		return
	}
	writeTxMonitorLog(txMonitorLogEntry{
		AcquiredAt: time.Now().Format(time.RFC3339Nano),
		Stage:      "confirmed_log",
		Source:     "chainlog",
		TxHash:     entry.TxHash.Hex(),
		BlockNum:   entry.BlockNumber,
		To:         entry.Address.Hex(),
		Method:     "topic_0x396d5e90",
		Token:      token.Hex(),
	})
}

func logTransactionMonitor(stage, source string, tx *types.Transaction) {
	if tx == nil || tx.To() == nil || tx.Value() == nil {
		return
	}
	if tx.Value().Cmp(txMonitorMinValue) <= 0 {
		return
	}

	var (
		method string
		token  common.Address
		ok     bool
	)
	switch to := *tx.To(); to {
	case txMonitorTargetSwap:
		method, token, ok = parseSwapMonitorTx(tx.Data())
	case txMonitorTargetBuy:
		method, token, ok = parseBuyMonitorTx(tx.Data())
	}
	if !ok {
		return
	}

	entry := txMonitorLogEntry{
		AcquiredAt: time.Now().Format(time.RFC3339Nano),
		Stage:      stage,
		Source:     source,
		TxHash:     tx.Hash().Hex(),
		To:         tx.To().Hex(),
		Method:     method,
		Token:      token.Hex(),
		ValueWei:   tx.Value().String(),
	}
	writeTxMonitorLog(entry)
}

func parseSwapMonitorTx(data []byte) (string, common.Address, bool) {
	if !hasSelector(data, txMonitorSwapSelector) {
		return "", common.Address{}, false
	}
	offset, ok := readUint64Arg(data, 2)
	if !ok {
		return "", common.Address{}, false
	}
	pathStart := 4 + int(offset)
	pathLen, ok := readUint64Word(data, pathStart)
	if !ok || pathLen == 0 {
		return "", common.Address{}, false
	}
	token, ok := readAddressWord(data, pathStart+32)
	if !ok {
		return "", common.Address{}, false
	}
	return "swapExactTokensForETHSupportingFeeOnTransferTokens", token, true
}

func parseBuyMonitorTx(data []byte) (string, common.Address, bool) {
	switch {
	case hasSelector(data, txMonitorBuySelector):
		token, ok := readAddressArg(data, 0)
		if !ok {
			return "", common.Address{}, false
		}
		return "buyTokenAMAP", token, true
	case hasSelector(data, txMonitorRouteSwapSel):
		token, ok := readLastAddressWord(data, 4+5*32)
		if !ok {
			return "", common.Address{}, false
		}
		return "swap", token, true
	default:
		return "", common.Address{}, false
	}
}

func parseConfirmedLogToken(data []byte) (common.Address, bool) {
	// The monitored event payload encodes the token address in the second word.
	return readAddressWord(data, 32)
}

func hasSelector(data []byte, selector []byte) bool {
	return len(data) >= 4 && bytes.Equal(data[:4], selector)
}

func readAddressArg(data []byte, argIndex int) (common.Address, bool) {
	return readAddressWord(data, 4+argIndex*32)
}

func readUint64Arg(data []byte, argIndex int) (uint64, bool) {
	return readUint64Word(data, 4+argIndex*32)
}

func readUint64Word(data []byte, wordStart int) (uint64, bool) {
	if wordStart < 0 || wordStart+32 > len(data) {
		return 0, false
	}
	return new(big.Int).SetBytes(data[wordStart : wordStart+32]).Uint64(), true
}

func readAddressWord(data []byte, wordStart int) (common.Address, bool) {
	if wordStart < 0 || wordStart+32 > len(data) {
		return common.Address{}, false
	}
	word := data[wordStart : wordStart+32]
	if !bytes.Equal(word[:12], make([]byte, 12)) {
		return common.Address{}, false
	}
	addr := common.BytesToAddress(word[12:])
	if addr == (common.Address{}) {
		return common.Address{}, false
	}
	return addr, true
}

func readLastAddressWord(data []byte, start int) (common.Address, bool) {
	for wordStart := len(data) - 32; wordStart >= start; wordStart -= 32 {
		addr, ok := readAddressWord(data, wordStart)
		if ok {
			return addr, true
		}
	}
	return common.Address{}, false
}

func writeTxMonitorLog(entry txMonitorLogEntry) {
	txMonitorFileOnce.Do(func() {
		txMonitorFile, txMonitorFileErr = os.OpenFile(txMonitorLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	})
	if txMonitorFileErr != nil {
		log.Warn("Failed to open tx monitor log", "path", txMonitorLogPath, "error", txMonitorFileErr)
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		log.Warn("Failed to marshal tx monitor log entry", "error", err)
		return
	}
	txMonitorFileMu.Lock()
	defer txMonitorFileMu.Unlock()

	if _, err := txMonitorFile.Write(append(line, '\n')); err != nil {
		log.Warn("Failed to write tx monitor log", "path", txMonitorLogPath, "error", err)
	}
}

func monitorConfirmedLogs(blockchain *core.BlockChain, stop <-chan struct{}) {
	logsCh := make(chan []*types.Log, 128)
	sub := blockchain.SubscribeLogsEvent(logsCh)
	defer unsubscribeAndDrainLogSub(sub, logsCh)

	for {
		select {
		case logs := <-logsCh:
			logConfirmedLogs(logs)
		case <-sub.Err():
			return
		case <-stop:
			return
		}
	}
}

func unsubscribeAndDrainLogSub(sub event.Subscription, logsCh chan []*types.Log) {
	sub.Unsubscribe()
	for {
		select {
		case <-logsCh:
		default:
			return
		}
	}
}

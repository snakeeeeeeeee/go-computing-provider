package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/swanchain/go-computing-provider/account"
	"github.com/swanchain/go-computing-provider/conf"
	"github.com/swanchain/go-computing-provider/internal/computing"
	"github.com/swanchain/go-computing-provider/wallet"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func createAccount(cpRepoPath, ownerAddress, beneficiaryAddress string, ubiFlag bool) error {
	chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
	if err != nil {
		return fmt.Errorf("get rpc url failed, error: %v", err)
	}

	localWallet, err := wallet.SetupWallet(wallet.WalletRepo)
	if err != nil {
		return fmt.Errorf("setup wallet failed, error: %v", err)
	}

	ki, err := localWallet.FindKey(ownerAddress)
	if err != nil || ki == nil {
		return fmt.Errorf("the address: %s, private key %v", ownerAddress, wallet.ErrKeyInfoNotFound)
	}

	client, err := ethclient.Dial(chainUrl)
	if err != nil {
		return fmt.Errorf("dial rpc connect failed, error: %v", err)
	}
	defer client.Close()

	privateKey, err := crypto.HexToECDSA(ki.PrivateKey)
	if err != nil {
		return err
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("failed to cast public key to ECDSA")
	}

	publicAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	nonce, err := client.PendingNonceAt(context.Background(), publicAddress)
	if err != nil {
		return err
	}

	suggestGasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		return err
	}

	chainId, _ := client.ChainID(context.Background())
	auth, err := bind.NewKeyedTransactorWithChainID(privateKey, chainId)
	if err != nil {
		return err
	}

	auth.Nonce = big.NewInt(int64(nonce))
	suggestGasPrice = suggestGasPrice.Mul(suggestGasPrice, big.NewInt(3))
	suggestGasPrice = suggestGasPrice.Div(suggestGasPrice, big.NewInt(2))
	auth.GasFeeCap = suggestGasPrice
	auth.Context = context.Background()

	nodeID := computing.GetNodeId(cpRepoPath)
	multiAddresses := conf.GetConfig().API.MultiAddress

	if strings.Contains(conf.GetConfig().API.MultiAddress, "<") || strings.Contains(conf.GetConfig().API.MultiAddress, "PUBLIC") {
		return fmt.Errorf("the multi-address field needs to be configured, by modify config.toml or computing-provider init")
	}

	var ubiTaskFlag uint8
	if ubiFlag || conf.GetConfig().UBI.UbiTask {
		ubiTaskFlag = 1
	}

	contractAddress, tx, _, err := account.DeployAccount(auth, client, nodeID, []string{multiAddresses}, ubiTaskFlag, common.HexToAddress(beneficiaryAddress))
	if err != nil {
		return fmt.Errorf("deploy cp account contract failed, error: %v", err)
	}

	err = os.WriteFile(filepath.Join(cpRepoPath, "account"), []byte(contractAddress.Hex()), 0666)
	if err != nil {
		return fmt.Errorf("write cp account contract address failed, error: %v", err)
	}

	fmt.Printf("Contract deployed! Address: %s\n", contractAddress.Hex())
	fmt.Printf("Transaction hash: %s\n", tx.Hash().Hex())

	var blockNumber string
	timeout := time.After(3 * time.Minute)
	ticker := time.Tick(3 * time.Second)
	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for transaction confirmation, tx: %s", tx.Hash().Hex())
		case <-ticker:
			receipt, err := client.TransactionReceipt(context.Background(), tx.Hash())
			if err != nil {
				if errors.Is(err, ethereum.NotFound) {
					continue
				}
				return err
			}

			if receipt != nil && receipt.Status == types.ReceiptStatusSuccessful {
				blockNumber = receipt.BlockNumber.String()
				err := DoSend(contractAddress.Hex(), blockNumber)
				if err != nil {
					return err
				}
				fmt.Println("computing-provider successfully initialized, you can now start it with 'computing-provider ubi daemon'")
				return nil
			} else if receipt != nil && receipt.Status == 0 {
				return err
			}
		}
	}
}

func changeMultiAddress(ownerAddress, multiAddr string) error {
	chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
	if err != nil {
		return fmt.Errorf("get rpc url failed, error: %v", err)
	}

	localWallet, err := wallet.SetupWallet(wallet.WalletRepo)
	if err != nil {
		return fmt.Errorf("setup wallet ubi failed, error: %v", err)
	}

	ki, err := localWallet.FindKey(ownerAddress)
	if err != nil || ki == nil {
		return fmt.Errorf("the address: %s, private key %v", ownerAddress, wallet.ErrKeyInfoNotFound)
	}

	client, err := ethclient.Dial(chainUrl)
	if err != nil {
		return fmt.Errorf("dial rpc connect failed, error: %v", err)
	}
	defer client.Close()

	cpStub, err := account.NewAccountStub(client, account.WithCpPrivateKey(ki.PrivateKey))
	if err != nil {
		return fmt.Errorf("create ubi task client failed, error: %v", err)
	}

	cpAccount, err := cpStub.GetCpAccountInfo()
	if err != nil {
		return fmt.Errorf("get cpAccount faile, error: %v", err)
	}
	if !strings.EqualFold(cpAccount.OwnerAddress, ownerAddress) {
		return fmt.Errorf("the owner address is incorrect. The owner on the chain is %s, and the current address is %s", cpAccount.OwnerAddress, ownerAddress)
	}

	submitUBIProofTx, err := cpStub.ChangeMultiAddress([]string{multiAddr})
	if err != nil {
		return fmt.Errorf("change multi-addr tx failed, error: %v", err)
	}
	fmt.Printf("ChangeMultiAddress: %s \n", submitUBIProofTx)
	return nil
}

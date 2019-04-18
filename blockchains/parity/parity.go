package parity

import (
	db "../../db"
	state "../../state"
	util "../../util"
	helpers "../helpers"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

var conf *util.Config

func init() {
	conf = util.GetConfig()
}

/*
Build builds out a fresh new ethereum test network
*/
func Build(details *db.DeploymentDetails, servers []db.Server, clients []*util.SshClient,
	buildState *state.BuildState) ([]string, error) {

	mux := sync.Mutex{}
	pconf, err := NewConf(details.Params)
	fmt.Printf("%#v\n", *pconf)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	buildState.SetBuildSteps(9 + (7 * details.Nodes))
	//Make the data directories
	err = helpers.AllNodeExecCon(servers, buildState, func(serverNum int, localNodeNum int, absoluteNodeNum int) error {
		_, err := clients[serverNum].DockerExec(localNodeNum, "mkdir -p /parity")
		return err
	})
	if err != nil {
		log.Println(err)
		return nil, err
	}
	buildState.IncrementBuildProgress()

	/**Create the Password file**/
	{
		var data string
		for i := 1; i <= details.Nodes; i++ {
			data += "second\n"
		}
		err = buildState.Write("passwd", data)
		if err != nil {
			log.Println(err)
			return nil, err
		}
	}
	buildState.IncrementBuildProgress()
	/**Copy over the password file**/
	err = helpers.CopyToAllNodes(servers, clients, buildState, "passwd", "/parity/")
	if err != nil {
		log.Println(err)
		return nil, err
	}
	buildState.IncrementBuildProgress()

	/**Create the wallets**/
	wallets := make([]string, details.Nodes)
	rawWallets := make([]string, details.Nodes)
	err = helpers.AllNodeExecCon(servers, buildState, func(serverNum int, localNodeNum int, absoluteNodeNum int) error {
		res, err := clients[serverNum].DockerExec(localNodeNum, "parity --base-path=/parity/ --password=/parity/passwd account new")
		if err != nil {
			log.Println(err)
			return err
		}

		if len(res) == 0 {
			return fmt.Errorf("account new returned an empty response")
		}

		mux.Lock()
		wallets[absoluteNodeNum] = res[:len(res)-1]
		mux.Unlock()

		res, err = clients[serverNum].DockerExec(localNodeNum, "bash -c 'cat /parity/keys/ethereum/*'")
		if err != nil {
			log.Println(err)
			return err
		}
		buildState.IncrementBuildProgress()

		mux.Lock()
		rawWallets[absoluteNodeNum] = strings.Replace(res, "\"", "\\\"", -1)
		mux.Unlock()
		return nil
	})
	if err != nil {
		log.Println(err)
		return nil, err
	}
	/***********************************************************SPLIT************************************************************/
	switch pconf.Consensus {
	case "ethash":
		err = setupPOW(details, servers, clients, buildState, pconf, wallets)
	case "poa":
		err = setupPOA(details, servers, clients, buildState, pconf, wallets)
	}
	if err != nil {
		log.Println(err)
		return nil, err
	}

	/***********************************************************SPLIT************************************************************/

	err = helpers.AllNodeExecCon(servers, buildState, func(serverNum int, localNodeNum int, absoluteNodeNum int) error {
		for i, rawWallet := range rawWallets {
			_, err = clients[serverNum].DockerExec(localNodeNum, fmt.Sprintf("bash -c 'echo \"%s\">/parity/account%d'", rawWallet, i))
			if err != nil {
				log.Println(err)
				return err
			}

			_, err = clients[serverNum].DockerExec(localNodeNum,
				fmt.Sprintf("parity --base-path=/parity/ --chain /parity/spec.json --password=/parity/passwd account import /parity/account%d", i))
			if err != nil {
				log.Println(err)
				return err
			}
		}
		buildState.IncrementBuildProgress()
		return nil
	})
	if err != nil {
		log.Println(err)
		return nil, err
	}

	//util.Write("tmp/config.toml",configToml)
	err = helpers.AllNodeExecCon(servers, buildState, func(serverNum int, localNodeNum int, absoluteNodeNum int) error {
		defer buildState.IncrementBuildProgress()
		return clients[serverNum].DockerExecdLog(localNodeNum,
			fmt.Sprintf(`parity --author=%s -c /parity/config.toml --chain=/parity/spec.json`, wallets[absoluteNodeNum]))
	})
	if err != nil {
		log.Println(err)
		return nil, err
	}
	//Start peering via curl
	time.Sleep(time.Duration(5 * time.Second))
	//Get the enode addresses
	enodes := make([]string, details.Nodes)
	err = helpers.AllNodeExecCon(servers, buildState, func(serverNum int, localNodeNum int, absoluteNodeNum int) error {
		enode := ""
		for len(enode) == 0 {
			ip := servers[serverNum].Ips[localNodeNum]
			res, err := clients[serverNum].KeepTryRun(
				fmt.Sprintf(
					`curl -sS -X POST http://%s:8545 -H "Content-Type: application/json" `+
						` -d '{ "method": "parity_enode", "params": [], "id": 1, "jsonrpc": "2.0" }'`,
					ip))

			if err != nil {
				log.Println(err)
				return err
			}
			var result map[string]interface{}

			err = json.Unmarshal([]byte(res), &result)
			if err != nil {
				log.Println(err)
				return err
			}
			fmt.Println(result)

			err = util.GetJSONString(result, "result", &enode)
			if err != nil {
				log.Println(err)
				return err
			}
		}
		buildState.IncrementBuildProgress()
		mux.Lock()
		enodes[absoluteNodeNum] = enode
		mux.Unlock()
		return nil
	})
	if err != nil {
		log.Println(err)
		return nil, err
	}

	err = helpers.AllNodeExecCon(servers, buildState, func(serverNum int, localNodeNum int, absoluteNodeNum int) error {
		ip := servers[serverNum].Ips[localNodeNum]
		for i, enode := range enodes {
			if i == absoluteNodeNum {
				continue
			}
			_, err := clients[serverNum].KeepTryRun(
				fmt.Sprintf(
					`curl -sS -X POST http://%s:8545 -H "Content-Type: application/json"  -d `+
						`'{ "method": "parity_addReservedPeer", "params": ["%s"], "id": 1, "jsonrpc": "2.0" }'`,
					ip, enode))
			buildState.IncrementBuildProgress()
			if err != nil {
				log.Println(err)
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Println(err)
		return nil, err
	}

	buildState.IncrementBuildProgress()
	if pconf.Consensus == "ethash" {
		return nil, peerWithGeth(clients[0], buildState, enodes)
	}
	return nil, nil
}

/***************************************************************************************************************************/

func Add(details db.DeploymentDetails, servers []db.Server, clients []*util.SshClient,
	newNodes map[int][]string, buildState *state.BuildState) ([]string, error) {
	return nil, nil
}

func setupPOA(details *db.DeploymentDetails, servers []db.Server, clients []*util.SshClient,
	buildState *state.BuildState, pconf *ParityConf, wallets []string) error {
	//Create the chain spec files
	spec, err := BuildPoaSpec(pconf, details.Files, wallets)
	if err != nil {
		log.Println(err)
		return err
	}

	err = helpers.CopyBytesToAllNodes(servers, clients, buildState, spec, "/parity/spec.json")
	if err != nil {
		log.Println(err)
		return err
	}

	//handle configuration file
	return helpers.CreateConfigs(servers, clients, buildState, "/parity/config.toml",
		func(serverNum int, localNodeNum int, absoluteNodeNum int) ([]byte, error) {
			configToml, err := BuildPoaConfig(pconf, details.Files, wallets, "/parity/passwd", absoluteNodeNum)
			if err != nil {
				log.Println(err)
				return nil, err
			}
			return []byte(configToml), nil
		})
}

func setupPOW(details *db.DeploymentDetails, servers []db.Server, clients []*util.SshClient,
	buildState *state.BuildState, pconf *ParityConf, wallets []string) error {
	//Start up the geth node
	err := setupGeth(clients[0], buildState, pconf, wallets)
	if err != nil {
		log.Println(err)
		return err
	}
	buildState.IncrementBuildProgress()

	//Create the chain spec files
	spec, err := BuildSpec(pconf, details.Files, wallets)
	if err != nil {
		log.Println(err)
		return err
	}
	//create config file
	configToml, err := BuildConfig(pconf, details.Files, wallets, "/parity/passwd")
	if err != nil {
		log.Println(err)
		return err
	}

	//Copy over the config file, spec file, and the accounts
	return helpers.CopyBytesToAllNodes(servers, clients, buildState,
		configToml, "/parity/config.toml",
		spec, "/parity/spec.json")
}

func setupGeth(client *util.SshClient, buildState *state.BuildState, pconf *ParityConf, wallets []string) error {

	gethConf, err := GethSpec(pconf, wallets)
	if err != nil {
		log.Println(err)
		return err
	}

	err = buildState.Write("genesis.json", gethConf)
	if err != nil {
		log.Println(err)
		return err
	}

	err = client.Scp("genesis.json", "/home/appo/genesis.json")
	if err != nil {
		log.Println(err)
		return err
	}
	buildState.Defer(func() { client.Run("rm /home/appo/genesis.json") })

	buildState.IncrementBuildProgress()

	_, err = client.FastMultiRun(
		"docker exec wb_service0 mkdir -p /geth",
		"docker cp /home/appo/genesis.json wb_service0:/geth/",
		"docker exec wb_service0 bash -c 'echo second >> /geth/passwd'")

	res, err := client.Run("docker exec wb_service0 geth --datadir /geth/ --password /geth/passwd account new")
	if err != nil {
		log.Println(err)
		return err
	}
	buildState.IncrementBuildProgress()

	addressPattern := regexp.MustCompile(`\{[A-z|0-9]+\}`)
	addresses := addressPattern.FindAllString(res, -1)
	if len(addresses) < 1 {
		return fmt.Errorf("Unable to get addresses")
	}
	address =  addresses[0][1 : len( addresses[0])-1]

	_, err = client.Run(
		fmt.Sprintf("docker exec wb_service0 geth --datadir /geth/ --networkid %d init /geth/genesis.json", pconf.NetworkId))
	if err != nil {
		log.Println(err)
		return err
	}

	buildState.IncrementBuildProgress()

	_, err = client.Run(fmt.Sprintf(`docker exec -d wb_service0 geth --datadir /geth/ --networkid %d --rpc  --rpcaddr 0.0.0.0`+
		` --rpcapi "admin,web3,db,eth,net,personal,miner,txpool" --rpccorsdomain "0.0.0.0" --unlock="%s"`+
		` --password /geth/passwd --etherbase %s --nodiscover`, pconf.NetworkId, address, address))
	if err != nil {
		log.Println(err)
		return err
	}
	if !pconf.DontMine {
		_, err = client.KeepTryRun(
			`curl -sS -X POST http://172.30.0.2:8545 -H "Content-Type: application/json" ` +
				` -d '{ "method": "miner_start", "params": [8], "id": 3, "jsonrpc": "2.0" }'`)
	} else {
		_, err = client.KeepTryRun(
			`curl -sS -X POST http://172.30.0.2:8545 -H "Content-Type: application/json" ` +
				` -d '{ "method": "miner_stop", "params": [], "id": 3, "jsonrpc": "2.0" }'`)
	}
	return err
}

func peerWithGeth(client *util.SshClient, buildState *state.BuildState, enodes []string) error {
	for _, enode := range enodes {
		_, err := client.KeepTryRun(
			fmt.Sprintf(
				`curl -sS -X POST http://172.30.0.2:8545 -H "Content-Type: application/json" `+
					` -d '{ "method": "admin_addPeer", "params": ["%s"], "id": 1, "jsonrpc": "2.0" }'`,
				enode))
		buildState.IncrementBuildProgress()
		if err != nil {
			log.Println(err)
			return err
		}
	}

	buildState.IncrementBuildProgress()
	return nil
}

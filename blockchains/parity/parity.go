package parity

import (
    "regexp"
    "context"
    "errors"
    "fmt"
    "time"
    "log"
    "strings"
    "encoding/json"
    db "../../db"
    state "../../state"
    util "../../util"
    "golang.org/x/sync/semaphore"
)

var conf *util.Config

func init() {
    conf = util.GetConfig()
}

/**
 * Build the Ethereum Test Network
 * @param  map[string]interface{}   data    Configuration Data for the network
 * @param  int      nodes       The number of nodes in the network
 * @param  []Server servers     The list of servers passed from build
 */
func Build(data map[string]interface{}, nodes int, servers []db.Server, clients []*util.SshClient,
           buildState *state.BuildState) ([]string, error) {
    //var mutex = &sync.Mutex{}
    var sem = semaphore.NewWeighted(conf.ThreadLimit)
    ctx := context.TODO()
    pconf, err := NewConf(data)
    fmt.Printf("%#v\n",*pconf)
    if err != nil {
        log.Println(err)
        return nil, err
    }

    
    buildState.SetBuildSteps(8 + (10 * nodes))
    defer func() {
        fmt.Printf("Cleaning up...")
        util.Rm("tmp")
        fmt.Printf("done\n")
    }()

 
    //Make the data directories
    for i, server := range servers {
        for j, _ := range server.Ips {
            res,err := clients[i].DockerExec(j,"mkdir -p /parity")
            if err != nil {
                log.Println(res)
                log.Println(err)
                return nil, err
            }
        }
    }
    buildState.IncrementBuildProgress()

    /**Create the Password file**/
    {
        var data string
        for i := 1; i <= nodes; i++ {
            data += "second\n"
        }
        err = util.Write("./passwd", data)
        if err != nil {
            log.Println(err)
            return nil, err
        }
    }
    buildState.IncrementBuildProgress()
    defer util.Rm("./passwd")
    /**Copy over the password file**/
    for i, server := range servers {
        err = clients[i].Scp("./passwd", "/home/appo/passwd")
        if err != nil {
            log.Println(err)
            return nil, err
        }
        defer clients[i].Run("rm /home/appo/passwd")

        for j, _ := range server.Ips {
            err = clients[i].DockerCp(j,"/home/appo/passwd","/parity/")
            if err != nil {
                log.Println(err)
                return nil, err
            }
            buildState.IncrementBuildProgress()
        }
    }
    

    /**Create the wallets**/
    wallets := []string{}
    rawWallets := []string{}
    for i, server := range servers {
        for j, _ := range server.Ips {
            res,err := clients[i].DockerExec(j,
                    fmt.Sprintf("parity --base-path=/parity/ --password=/parity/passwd account new"))
            if err != nil{
                log.Println(res)
                log.Println(err)
                return nil, err
            }
            if len(res) == 0 {
                return nil,errors.New("account new returned an empty response")
            }

            address := res[:len(res)-1]
            wallets = append(wallets,address)

            res,err = clients[i].DockerExec(j,"bash -c 'cat /parity/keys/ethereum/*'")
            if err != nil {
                log.Println(res)
                log.Println(err)
                return nil, err
            }
            buildState.IncrementBuildProgress()
            rawWallets = append(rawWallets,strings.Replace(res,"\"","\\\"",-1)) 
        }
    }
    //Start up the geth node
    {
        gethConf,err := GethSpec(pconf,wallets)
        if err != nil {
            log.Println(err)
            return nil, err
        }

        err = util.Write("genesis.json",gethConf)
        if err != nil {
            log.Println(err)
            return nil, err
        }
        defer util.Rm("genesis.json")

        err = clients[0].Scp("./genesis.json","/home/appo/genesis.json")
        if err != nil {
            log.Println(err)
            return nil, err
        }
        defer clients[0].Run("rm /home/appo/genesis.json")

        buildState.IncrementBuildProgress()

        res,err := clients[0].Run("docker exec wb_service0 mkdir -p /geth")
        if err != nil {
            log.Println(res)
            log.Println(err)
            return nil, err
        }

        res,err = clients[0].Run("docker cp /home/appo/genesis.json wb_service0:/geth/")
        if err != nil {
            log.Println(res)
            log.Println(err)
            return nil, err
        }
        res,err = clients[0].Run("docker exec wb_service0 bash -c 'echo second >> /geth/passwd'")
        if err != nil {
            log.Println(res)
            log.Println(err)
            return nil, err
        }

        buildState.IncrementBuildProgress()

        res,err = clients[0].Run("docker exec wb_service0 geth --datadir /geth/ --password /geth/passwd account new")
        if err != nil {
            log.Println(res)
            log.Println(err)
            return nil, err
        }

        addressPattern := regexp.MustCompile(`\{[A-z|0-9]+\}`)
        addresses := addressPattern.FindAllString(res,-1)
        if len(addresses) < 1 {
            return nil,errors.New("Unable to get addresses")
        }
        address := addresses[0]
        address = address[1:len(address)-1]

        res,err = clients[0].Run(
            fmt.Sprintf("docker exec wb_service0 geth --datadir /geth/ --networkid %d init /geth/genesis.json",pconf.NetworkId))
        if err != nil {
            log.Println(res)
            log.Println(err)
            return nil, err
        }

        buildState.IncrementBuildProgress()

        res,err = clients[0].Run(fmt.Sprintf(`docker exec -d wb_service0 geth --datadir /geth/ --networkid %d --rpc --rpcaddr 0.0.0.0`+
                            ` --rpcapi "admin,web3,db,eth,net,personal,miner,txpool" --rpccorsdomain "0.0.0.0" --mine --unlock="%s"`+
                            ` --password /geth/passwd --etherbase %s`,pconf.NetworkId,address,address))
        if err != nil {
            log.Println(res)
            log.Println(err)
            return nil, err
        }
        res,err = clients[0].KeepTryRun(
            `curl -sS -X POST http://172.30.0.2:8545 -H "Content-Type: application/json"  -d '{ "method": "miner_start", "params": [8], "id": 3, "jsonrpc": "2.0" }'`)
        
        if err != nil {
            log.Println(res)
            log.Println(err)
            return nil, err
        }
    }
    buildState.IncrementBuildProgress()

    //Create the chain spec files
    spec,err := BuildSpec(pconf,wallets);
    if err != nil {
        log.Println(err)
        return nil,err
    }

    err = util.Write("./spec.json",spec)
    if err != nil {
        log.Println(err)
        return nil,err
    }
    defer util.Rm("./spec.json")

    //create config file
    configToml,err := BuildConfig(pconf,wallets,"/parity/passwd")
    if err != nil {
        log.Println(err)
        return nil, err
    }

    err = util.Write("./config.toml",configToml)
    if err != nil {
        log.Println(err)
        return nil,err
    }
    defer util.Rm("./config.toml")

    //Copy over the config file, spec file, and the accounts
    node := 0
    for i, server := range servers {
        err = clients[i].Scp("./config.toml", "/home/appo/config.toml")
        if err != nil {
            log.Println(err)
            return nil, err
        }
        defer clients[i].Run("rm /home/appo/config.toml")

        err = clients[i].Scp("./spec.json", "/home/appo/spec.json")
        if err != nil {
            log.Println(err)
            return nil, err
        }
        defer clients[i].Run("rm /home/appo/spec.json")

        for j, _ := range server.Ips {
            err = clients[i].DockerCp(j,"/home/appo/spec.json","/parity/")
            if err != nil {
                log.Println(err)
                return nil, err
            }

            buildState.IncrementBuildProgress()

            err = clients[i].DockerCp(j,"/home/appo/config.toml","/parity/")
            if err != nil {
                log.Println(err)
                return nil, err
            }

            buildState.IncrementBuildProgress()

            for k,rawWallet := range rawWallets {

                _,err = clients[i].DockerExec(j,fmt.Sprintf("bash -c 'echo \"%s\">>/parity/account%d'",rawWallet,k))
                if err != nil {
                    log.Println(err)
                    return nil, err
                }
                defer clients[i].DockerExec(j,fmt.Sprintf("rm /parity/account%d",k))

                res,err := clients[i].DockerExec(j,
                    fmt.Sprintf("parity --base-path=/parity/ --chain /parity/spec.json --password=/parity/passwd account import /parity/account%d",k))
                if err != nil{
                    log.Println(res)
                    log.Println(err)
                    return nil, err
                }
            }
            buildState.IncrementBuildProgress()
            node++
        }
    }
    
    //util.Write("tmp/config.toml",configToml)
    node = 0
    for i, server := range servers {
        for j, _ := range server.Ips {
            sem.Acquire(ctx, 1)
            //fmt.Printf("-----------------------------  Starting NODE-%d  -----------------------------\n", node)

            go func(node int, i int,localNum int) {
                defer sem.Release(1)

                parityCmd := fmt.Sprintf(`parity --author=%s -c /parity/config.toml --chain=/parity/spec.json`,
                        wallets[node])

                res,err := clients[i].DockerExecd(localNum,parityCmd)

                if err != nil {
                    log.Println(res)
                    log.Println(err)
                    buildState.ReportError(err)
                    return
                }

                buildState.IncrementBuildProgress()
            }(node,i,j)
            node++
        }
    }

    err = sem.Acquire(ctx, conf.ThreadLimit)
    if err != nil {
        log.Println(err)
        return nil, err
    }
    //Start peering via curl
    time.Sleep(time.Duration(5 * time.Second))
    //Get the enode addresses
    enodes := []string{}
    for i, server := range servers {
        for _, ip := range server.Ips{
            var enode string
            for len(enode) == 0 {

                res,err := clients[i].KeepTryRun(
                    fmt.Sprintf(
                        `curl -sS -X POST http://%s:8545 -H "Content-Type: application/json"  -d '{ "method": "parity_enode", "params": [], "id": 1, "jsonrpc": "2.0" }'`,
                        ip))

                if err != nil {
                    log.Println(err)
                    return nil, err
                }
                var result map[string]interface{}

                err = json.Unmarshal([]byte(res),&result)
                if err != nil {
                    log.Println(err)
                    return nil, err
                }
                fmt.Println(result)
                
                err = util.GetJSONString(result, "result",&enode)
                if err != nil {
                    log.Println(err)
                    return nil, err
                }
            }
            buildState.IncrementBuildProgress()
            enodes = append(enodes,enode)
        }
    }
    node = 0
    for i, server := range servers {
        for _, ip := range server.Ips {
            for k,enode := range enodes {
                if k == node {
                    continue
                }
                res,err := clients[i].KeepTryRun(
                    fmt.Sprintf(
                        `curl -sS -X POST http://%s:8545 -H "Content-Type: application/json"  -d '{ "method": "parity_addReservedPeer", "params": ["%s"], "id": 1, "jsonrpc": "2.0" }'`,
                        ip,
                        enode))
                if err != nil {
                    log.Println(res)
                    log.Println(err)
                    return nil, err
                }
            }
            buildState.IncrementBuildProgress()
            node++
        }
    }
    
    buildState.IncrementBuildProgress()
    sem.Release(conf.ThreadLimit)
    if !buildState.ErrorFree() {
        return nil, buildState.GetError()
    }
    
    {
        for _,enode := range enodes {
            res,err := clients[0].KeepTryRun(
                fmt.Sprintf(
                    `curl -sS -X POST http://172.30.0.2:8545 -H "Content-Type: application/json"  -d '{ "method": "admin_addPeer", "params": ["%s"], "id": 1, "jsonrpc": "2.0" }'`,
                    enode))
            buildState.IncrementBuildProgress()
            if err != nil {
                log.Println(res)
                log.Println(err)
                return nil, err
            }
        }
        res,err := clients[0].KeepTryRun(
            `curl -sS -X POST http://172.30.0.2:8545 -H "Content-Type: application/json"  -d '{ "method": "miner_start", "params": [8], "id": 4, "jsonrpc": "2.0" }'`)
        
        if err != nil {
            log.Println(res)
            log.Println(err)
            return nil, err
        }
        buildState.IncrementBuildProgress()
    }
    return nil, nil
}

/***************************************************************************************************************************/

func Add(data map[string]interface{},nodes int,servers []db.Server,clients []*util.SshClient,
         newNodes map[int][]string,buildState *state.BuildState) ([]string,error) {
    return nil,nil
}

func MakeFakeAccounts(accs int) []string {
    out := make([]string, accs)
    for i := 1; i <= accs; i++ {
        acc := fmt.Sprintf("%X", i)
        for j := len(acc); j < 40; j++ {
            acc = "0" + acc
        }
        acc = "0x" + acc
        out[i-1] = acc
    }
    return out
}

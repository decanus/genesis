package util

import(
    "time"
    "math"
    "math/rand"
    "errors"
)

type Point struct{
    X   int     `json:"x"`
    Y   int     `json:"y"`
}

func Distances(pnts []Point) [][]float64 {
    out := make([][]float64,len(pnts))
    for i,_ := range pnts{
        out[i] = make([]float64,len(pnts))
    }

    for i,ipnt := range pnts{
        for j,jpnt := range pnts{
            if j == i {
                continue;
            }
            diffX := math.Abs(float64(ipnt.X - jpnt.X))
            diffY := math.Abs(float64(ipnt.Y - jpnt.Y))
            out[i][j] = math.Sqrt(math.Pow(diffX,2)+math.Pow(diffY,2))
        }
    }
    return out
}

/*
    Distribute generates a roughly uniform random distribution for connections
    among nodes. 
 */
func Distribute(nodes []string,dist []int) ([][]string,error){
    if len(nodes) < 2 {
        return nil,errors.New("Cannot distribute a series smaller than 1")
    }
    for _,d := range dist{
        if d >= len(nodes){
            return nil,errors.New("Cannot distribute among more nodes than those that are given")
        }
    }
    s1 := rand.NewSource(time.Now().UnixNano())
    r1 := rand.New(s1)

    out := [][]string{}
    for i, _ := range nodes {
        conns := []string{}
        for j := 0; j < dist[i]; j++ {
            newConnIndex := r1.Intn(len(nodes))
            if newConnIndex == i {
                j--
                continue
            }
            newConn := nodes[newConnIndex]
            unique := true
            for _, conn := range conns{
                if newConn == conn {
                    unique = false
                    break
                }
            }
            if !unique {
                j--
                continue
            }
            conns = append(conns,newConn)
        
        }
        out = append(out,conns)
    }
    return out,nil
}

/**
 * @brief Generates a random path through all nodes
 */
func GenerateworstCaseNetwork(nodes int) [][]int {
    out := make([][]int,nodes)

    s1 := rand.NewSource(time.Now().UnixNano())
    rng := rand.New(s1)

    nodePool := make([]int,nodes)
    for i := 0; i < nodes; i++ {
        nodePool[i] = i
    }
    node := 0
    for i := 0; i < nodes; i++ {
        newNodeIndex := rng.Intn(len(nodePool))
    newNode := nodePool[newNodeIndex]
        out[node] = []int{newNode}
        node = newNode
    nodePool = append(nodePool[:newNodeIndex],nodePool[newNodeIndex+1:]...)
    }
    out[node] = []int{0}
    return out
}

func GenerateUniformRandMeshNetwork(nodes int,conns int) ([][]int,error) {
    if conns < 1 {
        return nil,errors.New("Each node must have atleast one connection")
    }
    if conns >= nodes {
        return nil,errors.New("Too many connection to distribute without duplicates")
    } 
    s1 := rand.NewSource(time.Now().UnixNano())
    rng := rand.New(s1)
    out := GenerateworstCaseNetwork(nodes)


    for i := 0; i < nodes; i++ {
        for j := 1; j < conns; j++ {
            node := rng.Intn(nodes)
            add := true
            for _,node2 := range out[i] {
                if node == node2 {
                    j--
                    add = false
                    break
                }
            }
            if add {
                out[i] = append(out[i],node)
            }
            
        }
    }
    return out,nil
}
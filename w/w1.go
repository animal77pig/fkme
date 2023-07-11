package w

import (
	"bufio"
	"fmt"
	"github.com/lulugyf/fkme/logger"
	"io"
	"log"
	"os/exec"
	re "regexp"
	"strconv"
	"time"
)

func mon(args []string, ch chan string) {
	exefile := args[0]
	//cmd := exec.Command("tail", "-f", "/tmp/aa")
	cmd := exec.Command(exefile, args[1:]...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}

	err = cmd.Start()
	if err != nil {
		log.Fatal(err)
	}

	buffer := bufio.NewReader(stdout)
	for {
		line, _, err := buffer.ReadLine()
		//logger.Info("line: [%s]", string(line))
		ch <- string(line)
		if err == io.EOF {
			break
		}
	}

	p_stat, err := cmd.Process.Wait()
	logger.Warn("%s exited with code: %v err: %v", exefile, p_stat.ExitCode(), err)
}

func to_int(s []string) []int {
	ret := make([]int, len(s))
	for i, t := range s {
		n, err := strconv.Atoi(t)
		if err != nil {
			n = -1
		}
		ret[i] = n
	}
	return ret
}

type gpu_stat struct {
	Count int // card count
	Power int // current power in W
	Gtemp int // temperature in C
	Sm    int // 算力使用率 %
	Mem   int // 内存使用率 %
	Fb    int // 内存使用数 MB
}

func (c *gpu_stat) ToStr() string {
	// count,power,gtemp,sm(%),mem(%s),fb(MB)
	cc := c.Count
	return fmt.Sprintf("%d,%d,%d,%d,%d,%d",
		c.Count,
		c.Power/cc,
		c.Gtemp/cc, c.Sm/cc, c.Mem/cc, c.Fb)
}

/**
bash-4.2$ nvidia-smi dmon -s pucm -d 2
# gpu   pwr gtemp mtemp    sm   mem   enc   dec  mclk  pclk    fb  bar1
# Idx     W     C     C     %     %     %     %   MHz   MHz    MB    MB
    0    31    34     -     0     0     0     0   715  1189  3611     2
    1    32    31     -     0     0     0     0   715  1189  3011     2
    2    31    35     -     0     0     0     0   715  1189   289     2
    3    32    33     -     0     0     0     0   715  1189  2983     2
    0    31    34     -     0     0     0     0   715  1189  3611     2
    1    32    31     -     0     0     0     0   715  1189  3011     2
    2    31    35     -     0     0     0     0   715  1189   289     2
    3    32    32     -     0     0     0     0   715  1189  2983     2
*/
func merge_result(logfile string, ch chan string) {
	logger.InitLogger(logfile)

	lines := []string{}

	sp := re.MustCompile("\\s+")

	for {
		select {
		case line := <-ch:
			{
				lines = append(lines, line)
			}
		case <-time.After(time.Second * 2):
			{
				c := gpu_stat{}
				for _, line := range lines {
					nf := sp.Split(line, -1)
					if len(nf) < 5 {
						continue
					}
					//logger.Info("line [%s] nf0=[%s]", line, nf[1])
					_, err := strconv.Atoi(nf[1])
					if err != nil {
						continue
					}
					nn := to_int(nf)
					c.Power += nn[2]
					c.Gtemp += nn[3]
					c.Fb += nn[11]
					c.Sm += nn[5]
					c.Mem += nn[6]
					c.Count += 1
				}

				if len(lines) > 0 {
					if c.Count > 0 {
						logger.Info("%s", c.ToStr())
					}

					lines = []string{}
				}
			}
		}
	}
}

// ./fkme w1 /tmp/gpu.log -- nvidia-smi dmon -s pucm -d 2
func Run1(args []string) {
	//log.Printf("just test it\n")

	//startGPUMon("/tmp", "/tmp/logs", "")
	logfile := args[0]
	var p int = -1
	for i, n := range args {
		if n == "--" {
			p = i
			break
		}
	}
	p = p + 1
	exefile := args[p]
	if _, err := exec.LookPath(exefile); err != nil {
		log.Printf("%s not found, exit", exefile)
		return
	}
	ch := make(chan string, 5)
	go mon(args[p:], ch)
	merge_result(logfile, ch)
}

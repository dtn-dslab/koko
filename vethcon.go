package main

import (
	"os"
	"fmt"
	"net"
	"strings"
	"github.com/mattn/go-getopt"

	"github.com/vishvananda/netlink"
        "github.com/containernetworking/cni/pkg/ns"

	"github.com/docker/docker/client"
	"golang.org/x/net/context"
)


func makeVethPair(name, peer string, mtu int) (netlink.Link, error) {
        veth := &netlink.Veth{
                LinkAttrs: netlink.LinkAttrs{
                        Name:  name,
                        Flags: net.FlagUp,
                        MTU:   mtu,
                },
                PeerName: peer,
        }

        if err := netlink.LinkAdd(veth); err != nil {
                return nil, err
        }
        return veth, nil
}

func getVethPair(name1 string, name2 string) (link1 netlink.Link, link2 netlink.Link, err error) {

	link1, err = makeVethPair(name1, name2, 1500)
	if err != nil {
		switch {
		case os.IsExist(err):
			err = fmt.Errorf("container veth name provided (%v) already exists\n", name1)
			return
		default:
			err = fmt.Errorf("failed to make veth pair: %v\n", err)
			return
		}
	}

	link2, err = netlink.LinkByName(name2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to lookup %q: %v\n", name2, err)
	}

	return
}

func getDockerContainerNS(containerId string) (namespace string, err error) {
	ctx := context.Background()
	cli, err := client.NewEnvClient()
	if err != nil {
		panic(err)
	}

	cli.UpdateClientVersion("1.24")

	json, err := cli.ContainerInspect(ctx, containerId)
	if err != nil {
		err = fmt.Errorf("failed to get container info: %v\n", err)
		return
	}
	if json.NetworkSettings == nil {
		err = fmt.Errorf("failed to get container info: %v\n", err)
		return
	}
	namespace = json.NetworkSettings.NetworkSettingsBase.SandboxKey
	return
}

type vEth struct {
	nsName string
	linkName string
	withIPAddr bool
	ipAddr net.IPNet
}

func (veth *vEth) setVethLink (link netlink.Link) (err error) {
	vethNs, err := ns.GetNS(veth.nsName)

	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}
	defer vethNs.Close()

	if err := netlink.LinkSetNsFd(link, int(vethNs.Fd())); err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}

        err = vethNs.Do(func(_ ns.NetNS) error {
                link, err := netlink.LinkByName(veth.linkName)
                if err != nil {
                        return fmt.Errorf("failed to lookup %q in %q: %v", veth.linkName, vethNs.Path(), err)
                }

                if err = netlink.LinkSetUp(link); err != nil {
                        return fmt.Errorf("failed to set %q up: %v", veth.linkName, err)
                }

		if veth.withIPAddr {
			addr := &netlink.Addr{IPNet: &veth.ipAddr, Label:""}
			if err = netlink.AddrAdd(link, addr); err != nil {
				return fmt.Errorf("failed to add IP addr %v to %q: %v", addr, veth.linkName, err)
			}
		}

                return nil
	})

	return
}



func makeVeth (veth1 vEth, veth2 vEth) {

	link1, link2, err := getVethPair(veth1.linkName, veth2.linkName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}

	veth1.setVethLink(link1)
	veth2.setVethLink(link2)
}


func parseNOption (s string) (veth vEth, err error) {
	n := strings.Split(s, ":")
	if len(n) != 3 && len(n) != 2 {
		err = fmt.Errorf("failed to parse %s", s)
		return
	}

	veth.nsName = fmt.Sprintf("/var/run/netns/%s", n[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}

	veth.linkName = n[1]

	if len(n) == 3 {
		ip, mask, err2 := net.ParseCIDR(n[2])
		if err2 != nil {
			err = fmt.Errorf("failed to parse IP addr %s: %v",
			                 n[2], err2)
			return
		}
		veth.ipAddr.IP = ip
		veth.ipAddr.Mask = mask.Mask
		veth.withIPAddr = true
	} else {
		veth.withIPAddr = false
	}

	return
}

func parseDOption (s string) (veth vEth, err error) {
	n := strings.Split(s, ":")
	if len(n) != 3 && len(n) != 2 {
		err = fmt.Errorf("failed to parse %s", s)
		return
	}

	veth.nsName, err = getDockerContainerNS(n[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
	}

	veth.linkName = n[1]

	if len(n) == 3 {
		ip, mask, err2 := net.ParseCIDR(n[2])
		if err2 != nil {
			err = fmt.Errorf("failed to parse IP addr %s: %v",
			                 n[2], err2)
			return
		}
		veth.ipAddr.IP = ip
		veth.ipAddr.Mask = mask.Mask
		veth.withIPAddr = true
	} else {
		veth.withIPAddr = false
	}

	return
}

/*
 Usage:
 ./vethcon -d centos1:link1:192.168.1.1/24 -d centos2:link2:192.168.1.2/24 #with IP addr
 ./vethcon -d centos1:link1 -d centos2:link2  #without IP addr
 ./vethcon -n /var/run/netns/test1:link1:192.168.1.1/24 <other>
*/
func main() {

	var c int
	var err error

	cnt := 0
	getopt.OptErr = 0


	veth1 := vEth{}
	veth2 := vEth{}

	for {
		if c = getopt.Getopt("d:n:"); c == getopt.EOF {
			break
		}
		switch c {
		case 'd':
			if cnt == 0 {
				veth1, err = parseDOption(getopt.OptArg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Parse failed %s!:%v", getopt.OptArg, err)
					os.Exit(1)
				}
			} else if cnt == 1 {
				veth2, err = parseDOption(getopt.OptArg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Parse failed %s!:%v", getopt.OptArg, err)
					os.Exit(1)
				}
			} else {
				fmt.Fprintf(os.Stderr, "Too many config!")
				os.Exit(1)
			}
			cnt++

		case 'n':
			if cnt == 0 {
				veth1, err = parseNOption(getopt.OptArg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Parse failed %s!:%v", getopt.OptArg, err)
					os.Exit(1)
				}
			} else if cnt == 1 {
				veth2, err = parseNOption(getopt.OptArg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Parse failed %s!:%v", getopt.OptArg, err)
					os.Exit(1)
				}
			} else {
				fmt.Fprintf(os.Stderr, "Too many config!")
				os.Exit(1)
			}
			cnt++
		}
	}

	if cnt == 2 {
		fmt.Printf("Create veth...")
		makeVeth(veth1, veth2)
		fmt.Printf("done\n")
	}
}


/*
 * mysql-sniffer.go
 *
 * written by Mark Smith <mark@qq.is> --- Thanks!
 *
 * requires the gopcap library to be installed from:
 *   https://github.com/akrennmair/gopcap
 *
 */

package main

import (
	"time"
	"os"
	"flag"
	"fmt"
	"github.com/akrennmair/gopcap"
	"log"
	"math/rand"
	"sort"
	"strings"
)

const (
	TOKEN_WORD       = 0
	TOKEN_QUOTE      = 1
	TOKEN_NUMBER     = 2
	TOKEN_WHITESPACE = 3
	TOKEN_OTHER      = 4

	// Internal tuning
	TIME_BUCKETS = 10000

	// ANSI colors
	COLOR_RED     = "\x1b[31m"
	COLOR_GREEN   = "\x1b[32m"
	COLOR_YELLOW  = "\x1b[33m"
	COLOR_CYAN    = "\x1b[36m"
	COLOR_WHITE   = "\x1b[37m"
	COLOR_DEFAULT = "\x1b[39m"

	// MySQL packet types
	COM_QUERY = 3

	// These are used for formatting outputs
	F_NONE = iota
	F_QUERY
	F_ROUTE
	F_SOURCE
	F_SOURCEIP
)

type packet struct {
	request bool // request or response
	data    []byte
}

type sortable struct {
	value float64
	line  string
}
type sortableSlice []sortable

type source struct {
	src       string
	srcip     string
	synced    bool
	reqbuffer []byte
	resbuffer []byte
	reqSent   *time.Time
	reqTimes  [TIME_BUCKETS]uint64
	qbytes    uint64
	qdata     *queryData
	qtext     string
}

type connpacket struct {
	username string
	database string
}

type queryData struct {
	count uint64
	bytes uint64
	times [TIME_BUCKETS]uint64
}

var start int64 = UnixNow()
var qbuf map[string]*queryData = make(map[string]*queryData)
var querycount int
var chmap map[string]*source = make(map[string]*source)
var verbose bool = false
var dirty bool = false
var format []interface{}
var port uint16
var times [TIME_BUCKETS]uint64
var connections map[string]string = make(map[string]string)


var stats struct {
	packets struct {
		rcvd      uint64
		rcvd_sync uint64
	}
	desyncs uint64
	streams uint64
}

func UnixNow() int64 {
	return time.Now().Unix()
}

func main() {
	var lport *int = flag.Int("P", 3306, "MySQL port to use")
	var eth *string = flag.String("i", "eth0", "Interface to sniff")
	var ldirty *bool = flag.Bool("u", false, "Unsanitized -- do not canonicalize queries")
	var period *int = flag.Int("t", 10, "Seconds between outputting status")
	var displaycount *int = flag.Int("d", 15, "Display this many queries in status updates")
	var doverbose *bool = flag.Bool("v", false, "Print every query received (spammy)")
	var formatstr *string = flag.String("f", "#q", "Format for output aggregation")
	var sortby *string = flag.String("s", "count", "Sort by: count, max, avg, maxbytes, avgbytes")
	var cutoff *int = flag.Int("c", 0, "Only show queries over count/second")
	flag.Parse()


    	//logwriter, e := syslog.New(syslog.LOG_NOTICE | syslog.LOG_NEWS, "mysqlsniffer")
	//if e == nil {
	//   log.SetOutput(logwriter)
	//}

	verbose = *doverbose
	port = uint16(*lport)
	dirty = *ldirty
	parseFormat(*formatstr)
	rand.Seed(time.Now().UnixNano())

	log.SetPrefix("")
	log.SetFlags(0)

	log.Printf("Initializing MySQL sniffing on %s:%d...", *eth, port)
	iface, err := pcap.Openlive(*eth, 1024, false, 0)
	if iface == nil || err != nil {
		msg := "unknown error"
		if err != nil {
			msg = err.Error()
		}
		log.Fatalf("Failed to open device: %s", msg)
	}

	err = iface.Setfilter(fmt.Sprintf("tcp port %d", port))
	if err != nil {
		log.Fatalf("Failed to set port filter: %s", err.Error())
	}

	last := UnixNow()
	var pkt *pcap.Packet = nil
	var rv int32 = 0

	fmt.Println("Abro el archivo")
	f, err := os.OpenFile("/var/log/mysqlqueries.txt", os.O_CREATE|os.O_WRONLY, 0666)
	 if err != nil {
              panic(err)	
	 }
	 
	 


	for rv = 0; rv >= 0; {
		for pkt, rv = iface.NextEx(); pkt != nil; pkt, rv = iface.NextEx() {
			handlePacket(pkt,f)

			// simple output printer... this should be super fast since we expect that a
			// system like this will have relatively few unique queries once they're
			// canonicalized.
			if !verbose && querycount%1000 == 0 && last < UnixNow()-int64(*period) {
				last = UnixNow()
				handleStatusUpdate(*displaycount, *sortby, *cutoff)
			}
		}
	}
}

func calculateTimes(timings *[TIME_BUCKETS]uint64) (fmin, favg, fmax float64) {
	var counts, total, min, max, avg uint64 = 0, 0, 0, 0, 0
	has_min := false
	for _, val := range *timings {
		if val == 0 {
			// Queries should never take 0 nanoseconds. We are using 0 as a
			// trigger to mean 'uninitialized reading'.
			continue
		}
		if val < min || !has_min {
			has_min = true
			min = val
		}
		if val > max {
			max = val
		}
		counts++
		total += val
	}
	if counts > 0 {
		avg = total / counts // integer division
	}
	return float64(min) / 1000000, float64(avg) / 1000000,
		float64(max) / 1000000
}

func handleStatusUpdate(displaycount int, sortby string, cutoff int) {
	elapsed := float64(UnixNow() - start)

	// print status bar
	log.Printf("aaaaaaaaaaaaaaaaaaaaaaa\n")
	log.SetFlags(log.Ldate | log.Ltime)
	log.Printf("%s%d total queries, %0.2f per second%s", COLOR_RED, querycount,
		float64(querycount)/elapsed, COLOR_DEFAULT)
	log.SetFlags(0)

	log.Printf("%d packets (%0.2f%% on synchronized streams) / %d desyncs / %d streams",
		stats.packets.rcvd, float64(stats.packets.rcvd_sync)/float64(stats.packets.rcvd)*100,
		stats.desyncs, stats.streams)

	// global timing values
	gmin, gavg, gmax := calculateTimes(&times)
	log.Printf("%0.2fms min / %0.2fms avg / %0.2fms max query times", gmin, gavg, gmax)
	log.Printf("%d unique results in this filter", len(qbuf))
	log.Printf(" ")
	log.Printf("%s count     %sqps     %s  min    avg   max      %sbytes      per qry%s",
		COLOR_YELLOW, COLOR_CYAN, COLOR_YELLOW, COLOR_GREEN, COLOR_DEFAULT)

	// we cheat so badly here...
	var tmp sortableSlice = make(sortableSlice, 0, len(qbuf))
	for q, c := range qbuf {
		qps := float64(c.count) / elapsed
		if qps < float64(cutoff) {
			continue
		}

		qmin, qavg, qmax := calculateTimes(&c.times)
		bavg := uint64(float64(c.bytes) / float64(c.count))

		sorted := float64(c.count)
		if sortby == "avg" {
			sorted = qavg
		} else if sortby == "max" {
			sorted = qmax
		} else if sortby == "maxbytes" {
			sorted = float64(c.bytes)
		} else if sortby == "avgbytes" {
			sorted = float64(bavg)
		}

		tmp = append(tmp, sortable{sorted, fmt.Sprintf(
			"%s%6d  %s%7.2f/s  %s%6.2f %6.2f %6.2f  %s%9db %6db %s%s%s",
			COLOR_YELLOW, c.count, COLOR_CYAN, qps, COLOR_YELLOW, qmin, qavg, qmax,
			COLOR_GREEN, c.bytes, bavg, COLOR_WHITE, q, COLOR_DEFAULT)})
	}
	sort.Sort(tmp)

	// now print top to bottom, since our sorted list is sorted backwards
	// from what we want
	if len(tmp) < displaycount {
		displaycount = len(tmp)
	}
	for i := 1; i <= displaycount; i++ {
		//log.Printf(tmp[len(tmp)-i].line)
	}
}

// Do something with a packet for a source.
func processPacket(rs *source, request bool, data []byte,srcPort uint16, f *os.File, dstIP []byte) {
			//log.Printf("[%s] request=%t, got %d bytes", rs.src, request,
				// len(data))

	stats.packets.rcvd++
	if rs.synced {
		stats.packets.rcvd_sync++
	}

	var ptype int = -1
	var pdata []byte

	if request {
		// If we still have response buffer, we're in some weird state and
		// didn't successfully process the response.
		if rs.resbuffer != nil {
			//				log.Printf("[%s] possibly pipelined request? %d bytes",
			//					rs.src, len(rs.resbuffer))
			stats.desyncs++
			rs.resbuffer = nil
			rs.synced = false
		}
		rs.reqbuffer = data
		ptype, pdata = carvePacket(&rs.reqbuffer,f,rs,srcPort,dstIP)   // rs *source , srcPort uint16
	} else {
		// FIXME: For now we're not doing anything with response data, just using the first packet
		// after a query to determine latency.
		rs.resbuffer = nil
		ptype, pdata = 0, data
	}

	// The synchronization logic: if we're not presently, then we want to
	// keep going until we are capable of carving off of a request/query.
	if !rs.synced {
		if !(request && ptype == COM_QUERY) {
			rs.reqbuffer, rs.resbuffer = nil, nil
			return
		}
		rs.synced = true
	}
	//log.Printf("[%s] request=%b ptype=%d plen=%d", rs.src, request, ptype, len(pdata))

	// No (full) packet detected yet. Continue on our way.
	if ptype == -1 {
		return
	}
	plen := uint64(len(pdata))

	// If this is a response then we want to record the timing and
	// store it with this channel so we can keep track of that.
	var reqtime uint64
	if !request {
		// Keep adding the bytes we're getting, since this is probably still part of
		// an earlier response
		if rs.reqSent == nil {
			if rs.qdata != nil {
				rs.qdata.bytes += plen
			}
			return
		}
		reqtime = uint64(time.Since(*rs.reqSent).Nanoseconds())

		// We keep track of per-source, global, and per-query timings.
		randn := rand.Intn(TIME_BUCKETS)
		rs.reqTimes[randn] = reqtime
		times[randn] = reqtime
		if rs.qdata != nil {
			// This should never fail but it has. Probably because of a
			// race condition I need to suss out, or sharing between
			// two different goroutines. :(
			rs.qdata.times[randn] = reqtime
			rs.qdata.bytes += plen
		}
		rs.reqSent = nil

		// If we're in verbose mode, just dump statistics from this one.
		if verbose {
			//log.Printf("%s %s %d %d %0.2f\n",rs.src ,rs.qtext, rs.qbytes, plen,
			//	float64(reqtime)/1000000)
			fmt.Printf("%s %s\n",rs.src ,rs.qtext)
			//log.print(rs)
		}

		return
	}

	// This is for sure a request, so let's count it as one.
	if rs.reqSent != nil {
		//			log.Printf("[%s] ...sending two requests without a response?",
		//				rs.src)
	}
	tnow := time.Now()
	rs.reqSent = &tnow

	// Convert this request into whatever format the user wants.
	querycount++
	var text string

	//fmt.Printf("3) %s:%d En processPacket haciendo print de pdata => %s  \n" ,rs.srcip,srcPort,string(pdata))

	for _, item := range format {
		switch item.(type) {
		case int:
			switch item.(int) {
			case F_NONE:
				log.Fatalf("F_NONE in format string")
			case F_QUERY:
				if dirty {
					text += string(pdata)
				} else {
					text += cleanupQuery(pdata)
				}
			case F_ROUTE:
				// Routes are in the query like:
				//     SELECT /* hostname:route */ FROM ...
				// We remove the hostname so routes can be condensed.
				parts := strings.SplitN(string(pdata), " ", 5)
				if len(parts) >= 4 && parts[1] == "/*" && parts[3] == "*/" {
					if strings.Contains(parts[2], ":") {
						text += strings.SplitN(parts[2], ":", 2)[1]
					} else {
						text += parts[2]
					}
				} else {
					text += "(unknown) " + cleanupQuery(pdata)
				}
			case F_SOURCE:
				text += rs.src
			case F_SOURCEIP:
				text += rs.srcip
			default:
				log.Fatalf("Unknown F_XXXXXX int in format string")
			}
		case string:
			text += item.(string)
		default:
			log.Fatalf("Unknown type in format string")
		}
	}
	qdata, ok := qbuf[text]
	if !ok {
		qdata = &queryData{}
		qbuf[text] = qdata
	}
	qdata.count++
	qdata.bytes += plen
	rs.qtext, rs.qdata, rs.qbytes = text, qdata, plen
}

// carvePacket tries to pull a packet out of a slice of bytes. If so, it removes
// those bytes from the slice.
func carvePacket(buf *[]byte, f *os.File  ,rs *source , srcPort uint16, dstIP []byte ) (int, []byte) {
	//fmt.Printf("2) carvePacket => %s \n", buf)
	datalen := uint32(len(*buf))
	if datalen < 5 {
		return -1, nil
	}

	size := uint32((*buf)[0]) + uint32((*buf)[1])<<8 + uint32((*buf)[2])<<16
	if size == 0 || datalen < size+4 {
		return -1, nil
	}

	// Else, has some length, try to validate it.
	end := size + 4
	ptype := int((*buf)[4])
	data := (*buf)[5 :]
	if end >= datalen {
		*buf = nil
	} else {
		*buf = (*buf)[end:]
	}




	rot13 := func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r 
		case r == '-'  || r == ' ' || r == '-' || r == '*' || r == '?' || r == '=' || r == '_' || r == '\'':
			return r 
		case r == '('  || r == ')' || r == ',' || r == '.' || r == '\\' || r == '|' || r == '/' || r == '+':
			return r 		
		case r == ':' || r == ';' || r == '@':
			return r 	
		}
		return -1
	}


	rot14 := func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r 
		case r == '_':
			return r 	
		}
		return -1
	}


	     datalentwo := uint32(len(data))

	     pdstip := fmt.Sprintf("%d.%d.%d.%d", dstIP[0], dstIP[1], dstIP[2],dstIP[3])

	     var table string = ""
	     var firstword string = "undefined"

	     if datalentwo > 7 && string(data) != "SELECT VERSION()" {
	     	// if starts with @ and ends in mysql_native_password
	     	var query string = "aa"
	     	query = string(data)
	     	var lowerquery string = strings.ToLower(query)
	     	lowerquery = strings.Replace(lowerquery, "  ", " ", -1)
	     	if strings.Contains(lowerquery," ") {
	     		firstword = strings.Split(lowerquery," ")[0]
	     		//fmt.Printf("Firstword: %s\n", firstword)

	     		if firstword == "select" {
	     			if strings.Contains(lowerquery,"from") {
		     			table = strings.Split(lowerquery,"from")[1]
		     			if strings.Contains(table," "){
		     				table = strings.Split(table," ")[1]
		     			//	fmt.Printf("TABLA: %s \n",table)
		     			}
		     		}
	     		} else if firstword == "delete" {
	     			if strings.Contains(lowerquery,"from") {
		     			table = strings.Split(lowerquery,"from")[1]
		     			if strings.Contains(table," "){
		     				table = strings.Split(table," ")[1]
		     			//	fmt.Printf("TABLA: %s \n",table)
		     			}
		     		}
	     		} else if firstword == "insert"{
	     			if strings.Contains(lowerquery,"into") {
		     			table = strings.Split(lowerquery,"into")[1]
		     			if strings.Contains(table," "){
		     				table = strings.Split(table," ")[1]
		     				if strings.Contains(table,"(") {
		     					table = strings.Split(table,"(")[0]
		     			//		fmt.Printf("TABLA: %s \n",table)
		     				}
		     			}
		     		}
	     		} else if firstword == "update"{
	     			if strings.Contains(lowerquery," "){
	     				table = strings.Split(lowerquery," ")[1]
	     				//fmt.Printf("TABLA: %s \n",table)
	     			}
	     		}
	     	}

		query = strings.Map(rot13, query)

		if strings.Contains(query, "@") && strings.Contains(query, "mysql_native_password") { // es un login
			//fmt.Println("SE CONECTO UN USUARIO")
			//fmt.Printf("2.0) => %q\n", data )
			var convdata = data[31:]
			//fmt.Printf("2.1) => %q\n", convdata )
			convdata2 := fmt.Sprintf("%q", convdata)
			var user = strings.Split(convdata2, "\\")[0][1:]
			var userlen int = len(user)

			var database = data[64 + userlen - 11:]
			var database2 = strings.Map(rot14, string(database))
			database2 = strings.Split(database2, "mysql_native_password")[0]
			join := fmt.Sprintf("%s:%d:%s", rs.srcip,srcPort, pdstip)
			connections[join] = user + "," + database2


			return ptype, data
		// }
		//else if {// hace un "use"
		}
		join := fmt.Sprintf("%s:%d:%s", rs.srcip,srcPort, pdstip)
		tex := fmt.Sprintf("%s,%s,%d,%s,%s,%s,%s,%q\n", time.Now().Format("Mon Jan 2 15:04:05") , rs.srcip, srcPort, pdstip, connections[join], table, firstword, query)
	     	f.Write([]byte(tex))
	     	f.Sync()
	     	log.Print(tex)

	     	if querycount % 100 == 0 {
		     	elapsed := float64(UnixNow() - start)
		     	fmt.Printf("%s%d total queries, %0.2f per second%s   \n", COLOR_RED, querycount,
			float64(querycount)/elapsed, COLOR_DEFAULT)
			//fmt.Println(time.Now().Format("Mon Jan	 2 15:04:05") )
		}
	     }

	     f.Sync()

		//fmt.Printf("datalen=%d size=%d end=%d ptype=%d data=%d buf=%d  \n",
		//	datalen, size, end, ptype, len(data), len(*buf))

	return ptype, data
}

// extract the data... we have to figure out where it is, which means extracting data
// from the various headers until we get the location we want.  this is crude, but
// functional and it should be fast.
func handlePacket(pkt *pcap.Packet, f *os.File) {


	defer func() {
        if r := recover(); r != nil {
            fmt.Println("Recovered in handlePacket", r)
        }
	    }()


	// Ethernet frame has 14 bytes of stuff to ignore, so we start our root position here
	var pos byte = 14

	// Grab the src IP address of this packet from the IP header.
	srcIP := pkt.Data[pos+12 : pos+16]
	dstIP := pkt.Data[pos+16 : pos+20]

	// The IP frame has the header length in bits 4-7 of byte 0 (relative).
	pos += pkt.Data[pos] & 0x0F * 4

	// Grab the source port from the TCP header.
	srcPort := uint16(pkt.Data[pos])<<8 + uint16(pkt.Data[pos+1])
	dstPort := uint16(pkt.Data[pos+2])<<8 + uint16(pkt.Data[pos+3])

	// The TCP frame has the data offset in bits 4-7 of byte 12 (relative).
	pos += byte(pkt.Data[pos+12]) >> 4 * 4

	// If this is a 0-length payload, do nothing. (Any way to change our filter
	// to only dump packets with data?)
	if len(pkt.Data[pos:]) <= 0 {
		return
	}

	// This is either an inbound or outbound packet. Determine by seeing which
	// end contains our port. Either way, we want to put this on the channel of
	// the remote end.
	var src string
	var request bool = false
	if srcPort == port {
		src = fmt.Sprintf("%d.%d.%d.%d:%d", dstIP[0], dstIP[1], dstIP[2],
			dstIP[3], dstPort)
		//log.Printf("response to %s", src)
	} else if dstPort == port {
		src = fmt.Sprintf("%d.%d.%d.%d:%d", srcIP[0], srcIP[1], srcIP[2],
			srcIP[3], srcPort)
		request = true
		//log.Printf("request from %s %v", src , pkt.Data)
		//reader := bufio.NewReader(os.Stdin)
		//text, _ := reader.ReadString('\n')
		//fmt.Scanln(text)
		//cmd := exec.Command("read lala")
		//fmt.Println("1) handlePacket: %s:%d ==> %s",srcIP,srcPort,pkt.Data[pos:])
	} else {
		log.Fatalf("got packet src = %d, dst = %d", srcPort, dstPort)
	}

	// Get the data structure for this source, then do something.
	rs, ok := chmap[src]
	if !ok {
		srcip := src[0:strings.Index(src, ":")]
		rs = &source{src: src, srcip: srcip, synced: false}
		stats.streams++
		chmap[src] = rs
	}

	//log.Printf("rs es %v", rs)
	//log.Printf("request es %v", request)


	// Now with a source, process the packet.
	if dstPort == port {
	processPacket(rs, request, pkt.Data[pos:],srcPort,f,dstIP)
	}
}

// scans forward in the query given the current type and returns when we encounter
// a new type and need to stop scanning.  returns the size of the last token and
// the type of it.
func scanToken(query []byte) (length int, thistype int) {
	if len(query) < 1 {
		log.Fatalf("scanToken called with empty query")
	}

	// peek at the first byte, then loop
	b := query[0]
	switch {
	case b == 39 || b == 34: // '"
		started_with := b
		escaped := false
		for i := 1; i < len(query); i++ {
			switch query[i] {
			case started_with:
				if escaped {
					escaped = false
					continue
				}
				return i + 1, TOKEN_QUOTE
			case 92:
				escaped = true
			default:
				escaped = false
			}
		}
		return len(query), TOKEN_QUOTE

	case b >= 48 && b <= 57: // 0-9
		for i := 1; i < len(query); i++ {
			switch {
			case query[i] >= 48 && query[i] <= 57: // 0-9
				// do nothing
			default:
				return i, TOKEN_NUMBER
			}
		}
		return len(query), TOKEN_NUMBER

	case b == 32 || (b >= 9 && b <= 13): // whitespace
		for i := 1; i < len(query); i++ {
			switch {
			case query[i] == 32 || (query[i] >= 9 && query[i] <= 13):
				// Eat all whitespace
			default:
				return i, TOKEN_WHITESPACE
			}
		}
		return len(query), TOKEN_WHITESPACE

	case (b >= 65 && b <= 90) || (b >= 97 && b <= 122): // a-zA-Z
		for i := 1; i < len(query); i++ {
			switch {
			case query[i] >= 48 && query[i] <= 57:
				// Numbers, allow.
			case (query[i] >= 65 && query[i] <= 90) || (query[i] >= 97 && query[i] <= 122):
				// Letters, allow.
			case query[i] == 36 || query[i] == 95:
				// $ and _
			default:
				return i, TOKEN_WORD
			}
		}
		return len(query), TOKEN_WORD

	default: // everything else
		return 1, TOKEN_OTHER
	}

	// shouldn't get here
	log.Fatalf("scanToken failure: [%s]", query)
	return
}

func cleanupQuery(query []byte) string {
	// iterate until we hit the end of the query...
	//fmt.Printf("4) cleanupQuery: %s\n",query)
	var qspace []string
	for i := 0; i < len(query); {
		length, toktype := scanToken(query[i:])

		switch toktype {
		case TOKEN_WORD, TOKEN_OTHER:
			qspace = append(qspace, string(query[i:i+length]))

		case TOKEN_NUMBER, TOKEN_QUOTE:
			qspace = append(qspace, "?")

		case TOKEN_WHITESPACE:
			qspace = append(qspace, " ")

		default:
			log.Fatalf("scanToken returned invalid token type %d", toktype)
		}

		i += length
	}

	// Remove hostname from the route information if it's present
	tmp := strings.Join(qspace, "")

	parts := strings.SplitN(tmp, " ", 5)
	if len(parts) >= 5 && parts[1] == "/*" && parts[3] == "*/" {
		if strings.Contains(parts[2], ":") {
			tmp = parts[0] + " /* " + strings.SplitN(parts[2], ":", 2)[1] + " */ " + parts[4]
		}
	}

	return strings.Replace(tmp, "?, ", "", -1)
}

// parseFormat takes a string and parses it out into the given format slice
// that we later use to build up a string. This might actually be an overcomplicated
// solution?
func parseFormat(formatstr string) {
	formatstr = strings.TrimSpace(formatstr)
	if formatstr == "" {
		formatstr = "#b:#k"
	}

	is_special := false
	curstr := ""
	do_append := F_NONE
	for _, char := range formatstr {
		if char == '#' {
			if is_special {
				curstr += string(char)
				is_special = false
			} else {
				is_special = true
			}
			continue
		}

		if is_special {
			switch strings.ToLower(string(char)) {
			case "s":
				do_append = F_SOURCE
			case "i":
				do_append = F_SOURCEIP
			case "r":
				do_append = F_ROUTE
			case "q":
				do_append = F_QUERY
			default:
				curstr += "#" + string(char)
			}
			is_special = false
		} else {
			curstr += string(char)
		}

		if do_append != F_NONE {
			if curstr != "" {
				format = append(format, curstr, do_append)
				curstr = ""
			} else {
				format = append(format, do_append)
			}
			do_append = F_NONE
		}
	}
	if curstr != "" {
		format = append(format, curstr)
	}
}

func (self sortableSlice) Len() int {
	return len(self)
}

func (self sortableSlice) Less(i, j int) bool {
	return self[i].value < self[j].value
}

func (self sortableSlice) Swap(i, j int) {
	self[i], self[j] = self[j], self[i]
}

package aim

import (
	"encoding/json"
	"fmt"
	"github.com/twpayne/gogeom/geom"
	"github.com/twpayne/gogeom/geom/encoding/geojson"
	"io/ioutil"
	"math"
	"os"
	"strings"
	"sync"
)

type AIMdata struct {
	Data          []*AIMcell // One data holder for each grid cell
	Dt            float64    // seconds
	nLayers       int        // number of model layers
	layerStart    []int      // start index of each layer (inclusive)
	layerEnd      []int      // end index of each layer (exclusive)
	westBoundary  []*AIMcell // boundary cells
	eastBoundary  []*AIMcell // boundary cells
	northBoundary []*AIMcell // boundary cells
	southBoundary []*AIMcell // boundary cells
	topBoundary   []*AIMcell // boundary cells; assume bottom boundary is the same as lowest layer
}

// Data for a single grid cell
type AIMcell struct {
	geom                           geom.T       // Cell geometry
	UPlusSpeed, UMinusSpeed        float64      // [m/s]
	VPlusSpeed, VMinusSpeed        float64      // [m/s]
	WPlusSpeed, WMinusSpeed        float64      // [m/s]
	OrgPartitioning, SPartitioning float64      // gaseous fraction
	NOPartitioning, NHPartitioning float64      // gaseous fraction
	ParticleWetDep, SO2WetDep      float64      // wet deposition rate [1/s]
	OtherGasWetDep                 float64      // wet deposition rate [1/s]
	ParticleDryDep, NH3DryDep      float64      // Dry deposition velocities [m/s]
	SO2DryDep, VOCDryDep           float64      // Dry deposition velocities [m/s]
	NOxDryDep                      float64      // Dry deposition velocities [m/s]
	SO2oxidation                   float64      // SO2 oxidation to SO4 by HO and H2O2 [1/s]
	Kzz                            float64      // Grid center vertical diffusivity after applying convective fraction [m2/s]
	KzzAbove, KzzBelow             []float64    // horizontal diffusivity [m2/s] (staggered grid)
	Kyyxx                          float64      // Grid center horizontal diffusivity [m2/s]
	KyySouth, KyyNorth             []float64    // horizontal diffusivity [m2/s] (staggered grid)
	KxxWest, KxxEast               []float64    // horizontal diffusivity at [m2/s] (staggered grid)
	M2u                            float64      // ACM2 upward mixing (Pleim 2007) [1/s]
	M2d                            float64      // ACM2 downward mixing (Pleim 2007) [1/s]
	PblTopLayer                    float64      // k index of boundary layer top
	Dx, Dy, Dz                     float64      // grid size [meters]
	Volume                         float64      // [cubic meters]
	Row                            int          // master cell index
	Ci                             []float64    // concentrations at beginning of time step [μg/m3]
	Cˣ, Cˣˣ                        []float64    // concentrations after first and second Runge-Kutta passes [μg/m3]
	Cf                             []float64    // concentrations at end of time step [μg/m3]
	emisFlux                       []float64    //  emissions [μg/m3/s]
	West                           []*AIMcell   // Neighbors to the East
	East                           []*AIMcell   // Neighbors to the West
	South                          []*AIMcell   // Neighbors to the South
	North                          []*AIMcell   // Neighbors to the North
	Below                          []*AIMcell   // Neighbors below
	Above                          []*AIMcell   // Neighbors above
	GroundLevel                    []*AIMcell   // Neighbors at ground level
	WestFrac, EastFrac             []float64    // Fraction of cell covered by each neighbor (adds up to 1).
	NorthFrac, SouthFrac           []float64    // Fraction of cell covered by each neighbor (adds up to 1).
	AboveFrac, BelowFrac           []float64    // Fraction of cell covered by each neighbor (adds up to 1).
	GroundLevelFrac                []float64    // Fraction of cell above to each ground level cell (adds up to 1).
	iWest                          []int        // Row indexes of neighbors to the East
	iEast                          []int        // Row indexes of neighbors to the West
	iSouth                         []int        // Row indexes of neighbors to the South
	iNorth                         []int        // Row indexes of neighbors to the north
	iBelow                         []int        // Row indexes of neighbors below
	iAbove                         []int        // Row indexes of neighbors above
	iGroundLevel                   []int        // Row indexes of neighbors at ground level
	DxPlusHalf                     []float64    // Distance between centers of cell and East [m]
	DxMinusHalf                    []float64    // Distance between centers of cell and West [m]
	DyPlusHalf                     []float64    // Distance between centers of cell and North [m]
	DyMinusHalf                    []float64    // Distance between centers of cell and South [m]
	DzPlusHalf                     []float64    // Distance between centers of cell and Above [m]
	DzMinusHalf                    []float64    // Distance between centers of cell and Below [m]
	Layer                          int          // layer index of grid cell
	LayerHeight                    float64      // heights at bottom edge of grid cell, m
	Temperature                    float64      // Average temperature, K
	WindSpeed                      float64      // RMS wind speed, [m/s]
	S1                             float64      // stability parameter [?]
	SClass                         float64      // stability class: "0=Unstable; 1=Stable
	lock                           sync.RWMutex // Avoid cell being written by one subroutine and read by another at the same time.
}

func (c *AIMcell) prepare() {
	c.Volume = c.Dx * c.Dy * c.Dz
	c.Ci = make([]float64, len(polNames))
	c.Cf = make([]float64, len(polNames))
	c.Cˣ = make([]float64, len(polNames))
	c.Cˣˣ = make([]float64, len(polNames))
	c.emisFlux = make([]float64, len(polNames))
}

func (c *AIMcell) makecopy() *AIMcell {
	c2 := new(AIMcell)
	c2.Dx, c2.Dy, c2.Dz = c.Dx, c.Dy, c.Dz
	c2.Kyyxx = c.Kyyxx
	c2.prepare()
	return c2
}

// Initialize the model, where `filename` is the path to
// the GeoJSON files with meteorology and background concentration data
// (where `[layer]` is a stand-in for the layer number),
// `nLayers` is the number of vertical layers in the model,
// and `httpPort` is the port number for hosting the html GUI.
func InitAIMdata(filetemplate string, nLayers int, httpPort string) *AIMdata {
	var err error
	type dataHolder struct {
		Type       string
		Geometry   *geojson.Geometry
		Properties *AIMcell
	}
	type dataHolderHolder struct {
		Proj4, Type string
		Features    []dataHolder
	}
	inputData := make([]*dataHolderHolder, nLayers)
	ncells := 0
	d := new(AIMdata)
	d.nLayers = nLayers
	d.layerStart = make([]int, nLayers)
	d.layerEnd = make([]int, nLayers)
	for k := 0; k < nLayers; k++ {
		filename := strings.Replace(filetemplate, "[layer]",
			fmt.Sprintf("%v", k), -1)
		f, err := os.Open(filename)
		if err != nil {
			panic(err)
		}
		buf, err := ioutil.ReadAll(f)
		if err != nil {
			panic(err)
		}
		fmt.Println("buf", len(buf))
		var dhh dataHolderHolder
		err = json.Unmarshal(buf, &dhh)
		inputData[k] = &dhh
		d.layerStart[k] = ncells
		fmt.Println(dhh.Features[10].Type)
		fmt.Println(len(dhh.Features))
		//fmt.Println(dhh.Features[0].Properties.Row)
		ncells += len(dhh.Features)
		d.layerEnd[k] = ncells
		f.Close()
	}
	// set up data holders
	d.Data = make([]*AIMcell, ncells)
	for _, indata := range inputData {
		for _, feature := range indata.Features {
			fmt.Println(feature.Properties)
			c := feature.Properties
			c.prepare()
			c.geom, err = geojson.FromGeoJSON(feature.Geometry)
			if err != nil {
				panic(err)
			}
			d.Data[c.Row] = c
		}
	}
	d.westBoundary = make([]*AIMcell, 0)
	d.eastBoundary = make([]*AIMcell, 0)
	d.southBoundary = make([]*AIMcell, 0)
	d.northBoundary = make([]*AIMcell, 0)
	d.topBoundary = make([]*AIMcell, 0)
	for _, cell := range d.Data {
		// Link cells to neighbors and/or boundaries.
		if len(cell.iWest) == 0 {
			c := cell.makecopy()
			cell.West = []*AIMcell{c}
			d.westBoundary = append(d.westBoundary, c)
		} else {
			cell.West = make([]*AIMcell, len(cell.iWest))
			for i, row := range cell.iWest {
				cell.West[i] = d.Data[row]
			}
			cell.iWest = nil
		}
		if len(cell.iEast) == 0 {
			c := cell.makecopy()
			cell.East = []*AIMcell{c}
			d.eastBoundary = append(d.eastBoundary, c)
		} else {
			cell.East = make([]*AIMcell, len(cell.iEast))
			for i, row := range cell.iEast {
				cell.East[i] = d.Data[row]
			}
			cell.iEast = nil
		}
		if len(cell.iSouth) == 0 {
			c := cell.makecopy()
			cell.South = []*AIMcell{c}
			d.southBoundary = append(d.southBoundary, c)
		} else {
			cell.South = make([]*AIMcell, len(cell.iSouth))
			for i, row := range cell.iSouth {
				cell.South[i] = d.Data[row]
			}
			cell.iSouth = nil
		}
		if len(cell.iNorth) == 0 {
			c := cell.makecopy()
			cell.North = []*AIMcell{c}
			d.northBoundary = append(d.northBoundary, c)
		} else {
			cell.North = make([]*AIMcell, len(cell.iNorth))
			for i, row := range cell.iNorth {
				cell.North[i] = d.Data[row]
			}
			cell.iNorth = nil
		}
		if len(cell.iAbove) == 0 {
			c := cell.makecopy()
			cell.Above = []*AIMcell{c}
			d.topBoundary = append(d.topBoundary, c)
		} else {
			cell.Above = make([]*AIMcell, len(cell.iAbove))
			for i, row := range cell.iAbove {
				cell.Above[i] = d.Data[row]
			}
			cell.iAbove = nil
		}
		if cell.Layer != 0 {
			cell.Below = make([]*AIMcell, len(cell.iBelow))
			cell.GroundLevel = make([]*AIMcell, len(cell.iGroundLevel))
			for i, row := range cell.iBelow {
				cell.Below[i] = d.Data[row]
			}
			for i, row := range cell.iGroundLevel {
				cell.GroundLevel[i] = d.Data[row]
			}
			cell.iBelow = nil
			cell.iGroundLevel = nil
		} else { // assume bottom boundary is the same as lowest layer.
			cell.Below = []*AIMcell{d.Data[cell.Row]}
			cell.GroundLevel = []*AIMcell{d.Data[cell.Row]}
		}
		cell.neighborInfo()
	}
	go d.WebServer(httpPort)
	return d
}

// Calculate center-to-center cell distance,
// fractions of grid cell covered by each neighbor
// and harmonic mean staggered-grid diffusivities.
func (cell *AIMcell) neighborInfo() {
	cell.DxPlusHalf = make([]float64, len(cell.East))
	cell.EastFrac = make([]float64, len(cell.East))
	cell.KxxEast = make([]float64, len(cell.East))
	for i, c := range cell.East {
		cell.DxPlusHalf[i] = (cell.Dx + c.Dx) / 2.
		cell.EastFrac[i] = min(c.Dy/cell.Dy, 1.)
		cell.KxxEast[i] = harmonicMean(cell.Kyyxx, c.Kyyxx)
	}
	cell.DxMinusHalf = make([]float64, len(cell.West))
	cell.WestFrac = make([]float64, len(cell.West))
	cell.KxxWest = make([]float64, len(cell.West))
	for i, c := range cell.West {
		cell.DxMinusHalf[i] = (cell.Dx + c.Dx) / 2.
		cell.WestFrac[i] = min(c.Dy/cell.Dy, 1.)
		cell.KxxWest[i] = harmonicMean(cell.Kyyxx, c.Kyyxx)
	}
	cell.DyPlusHalf = make([]float64, len(cell.North))
	cell.NorthFrac = make([]float64, len(cell.North))
	cell.KyyNorth = make([]float64, len(cell.North))
	for i, c := range cell.North {
		cell.DyPlusHalf[i] = (cell.Dy + c.Dy) / 2.
		cell.NorthFrac[i] = min(c.Dx/cell.Dx, 1.)
		cell.KyyNorth[i] = harmonicMean(cell.Kyyxx, c.Kyyxx)
	}
	cell.DyMinusHalf = make([]float64, len(cell.South))
	cell.SouthFrac = make([]float64, len(cell.South))
	cell.KyySouth = make([]float64, len(cell.South))
	for i, c := range cell.South {
		cell.DyMinusHalf[i] = (cell.Dy + c.Dy) / 2.
		cell.SouthFrac[i] = min(c.Dx/cell.Dx, 1.)
		cell.KyySouth[i] = harmonicMean(cell.Kyyxx, c.Kyyxx)
	}
	cell.DzPlusHalf = make([]float64, len(cell.Above))
	cell.AboveFrac = make([]float64, len(cell.Above))
	cell.KzzAbove = make([]float64, len(cell.Above))
	for i, c := range cell.Above {
		cell.DzPlusHalf[i] = (cell.Dz + c.Dz) / 2.
		cell.AboveFrac[i] = min((c.Dx*c.Dy)/(cell.Dx*cell.Dy), 1.)
		cell.KzzAbove[i] = harmonicMean(cell.Kzz, c.Kzz)
	}
	cell.DzMinusHalf = make([]float64, len(cell.Below))
	cell.BelowFrac = make([]float64, len(cell.Below))
	cell.KzzBelow = make([]float64, len(cell.Below))
	for i, c := range cell.Below {
		cell.DzMinusHalf[i] = (cell.Dz + c.Dz) / 2.
		cell.BelowFrac[i] = min((c.Dx*c.Dy)/(cell.Dx*cell.Dy), 1.)
		cell.KzzBelow[i] = harmonicMean(cell.Kzz, c.Kzz)
	}
	cell.GroundLevelFrac = make([]float64, len(cell.GroundLevel))
	for i, c := range cell.GroundLevel {
		cell.GroundLevelFrac[i] = min((c.Dx*c.Dy)/(cell.Dx*cell.Dy), 1.)
	}
}

// Add in emissions flux to each cell at every time step, also
// set initial concentrations to final concentrations from previous
// time step, and set old velocities to velocities from previous time
// step.
func (c *AIMcell) addEmissionsFlux(d *AIMdata) {
	for i, _ := range polNames {
		c.Cf[i] += c.emisFlux[i] * d.Dt
		c.Ci[i] = c.Cf[i]
	}
}

//  Set the time step using the Courant–Friedrichs–Lewy (CFL) condition.
func (d *AIMdata) setTstepCFL(nprocs int) {
	const Cmax = 1.5 // From Wicker and Skamarock (2002) Table 1.
	valChan := make(chan float64)
	calcCFL := func(procNum int) {
		var thisval, val float64
		var c *AIMcell
		for ii := procNum; ii < len(d.Data); ii += nprocs {
			c = d.Data[ii]
			thisval = max(c.UPlusSpeed/c.Dx, c.UMinusSpeed/c.Dx,
				c.VPlusSpeed/c.Dy, c.VMinusSpeed/c.Dy,
				c.WPlusSpeed/c.Dz, c.WMinusSpeed/c.Dz)
			if thisval > val {
				val = thisval
			}
		}
		valChan <- val
	}
	for procNum := 0; procNum < nprocs; procNum++ {
		go calcCFL(procNum)
	}
	val := 0.
	for i := 0; i < nprocs; i++ { // get max value from each processor
		procval := <-valChan
		if procval > val {
			val = procval
		}
	}
	d.Dt = Cmax / math.Pow(3., 0.5) / val // seconds
}

//  Set the time step using the WRF rule of thumb.
func (d *AIMdata) setTstepRuleOfThumb() {
	d.Dt = d.Data[0].Dx / 1000. * 6
}

func harmonicMean(a, b float64) float64 {
	return 2. * a * b / (a + b)
}

// Convert the concentration data into a regular array
func (d *AIMdata) toArray(pol string, layer int) []float64 {
	o := make([]float64, d.layerEnd[layer]-d.layerStart[layer])
	for i, c := range d.Data[d.layerStart[layer]:d.layerEnd[layer]] {
		c.lock.RLock()
		switch pol {
		case "VOC":
			o[i] = c.Cf[igOrg]
		case "SOA":
			o[i] = c.Cf[ipOrg]
		case "PrimaryPM2_5":
			o[i] = c.Cf[iPM2_5]
		case "NH3":
			o[i] = c.Cf[igNH] / NH3ToN
		case "pNH4":
			o[i] = c.Cf[ipNH] * NtoNH4
		case "SOx":
			o[i] = c.Cf[igS] / SOxToS
		case "pSO4":
			o[i] = c.Cf[ipS] * StoSO4
		case "NOx":
			o[i] = c.Cf[igNO] / NOxToN
		case "pNO3":
			o[i] = c.Cf[ipNO] * NtoNO3
		case "VOCemissions":
			o[i] = c.emisFlux[igOrg]
		case "NOxemissions":
			o[i] = c.emisFlux[igNO]
		case "NH3emissions":
			o[i] = c.emisFlux[igNH]
		case "SOxemissions":
			o[i] = c.emisFlux[igS]
		case "PM2_5emissions":
			o[i] = c.emisFlux[iPM2_5]
		case "UPlusSpeed":
			o[i] = c.UPlusSpeed
		case "UMinusSpeed":
			o[i] = c.UMinusSpeed
		case "VPlusSpeed":
			o[i] = c.VPlusSpeed
		case "VMinusSpeed":
			o[i] = c.VMinusSpeed
		case "WPlusSpeed":
			o[i] = c.WPlusSpeed
		case "WMinusSpeed":
			o[i] = c.WMinusSpeed
		case "Organicpartitioning":
			o[i] = c.OrgPartitioning
		case "Sulfurpartitioning":
			o[i] = c.SPartitioning
		case "Nitratepartitioning":
			o[i] = c.NOPartitioning
		case "Ammoniapartitioning":
			o[i] = c.NHPartitioning
		case "Particlewetdeposition":
			o[i] = c.ParticleWetDep
		case "SO2wetdeposition":
			o[i] = c.SO2WetDep
		case "Non-SO2gaswetdeposition":
			o[i] = c.OtherGasWetDep
		case "Kyyxx":
			o[i] = c.Kyyxx
		case "Kzz":
			o[i] = c.Kzz
		case "M2u":
			o[i] = c.M2u
		case "M2d":
			o[i] = c.M2d
		case "PblTopLayer":
			o[i] = c.PblTopLayer
		default:
			panic(fmt.Sprintf("Unknown variable %v.", pol))
		}
		c.lock.RUnlock()
	}
	return o
}

func (d *AIMdata) getGeometry(layer int) []geom.T {
	o := make([]geom.T, d.layerEnd[layer]-d.layerStart[layer])
	for i, c := range d.Data[d.layerStart[layer]:d.layerEnd[layer]] {
		o[i] = c.geom
	}
	return o
}

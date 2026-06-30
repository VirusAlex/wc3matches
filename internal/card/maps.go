package card

import "strings"

// mapImageURL returns a Liquipedia thumbnail URL for a map name, or "" when we
// have no mapping. In that case the card just omits the image; a missing or 404
// image would otherwise reject the whole rich message.
//
// Liquipedia only serves pre-generated thumbnail sizes, so these are fixed
// URLs. Extend this table as the competitive map pool changes.
func mapImageURL(name string) string {
	return mapImages[normalizeMap(name)]
}

func normalizeMap(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

const liqImg = "https://liquipedia.net/commons/images/thumb/"

var mapImages = map[string]string{
	"autumn leaves":       liqImg + "e/e8/Wc3AutumnLeaves.png/140px-Wc3AutumnLeaves.png",
	"concealed hill":      liqImg + "8/8f/Concealed_Hill_1.2.png/114px-Concealed_Hill_1.2.png",
	"gnoll wood":          liqImg + "e/eb/Wc3GnollWood.png/140px-Wc3GnollWood.png",
	"hammerfall":          liqImg + "2/21/Hammerfall.png/139px-Hammerfall.png",
	"last refuge":         liqImg + "9/9a/Last_Refuge.png/140px-Last_Refuge.png",
	"lost temple lv":      liqImg + "f/fd/Wc3LostTemple_LV.png/140px-Wc3LostTemple_LV.png",
	"northern isles":      liqImg + "4/45/Northern_Isles.png/177px-Northern_Isles.png",
	"shattered exile":     liqImg + "7/77/Wc3ShatteredExile.png/149px-Wc3ShatteredExile.png",
	"springtime":          liqImg + "c/c1/Wc3SpringTime.png/140px-Wc3SpringTime.png",
	"spring time":         liqImg + "c/c1/Wc3SpringTime.png/140px-Wc3SpringTime.png",
	"tidehunters":         liqImg + "6/60/Wc3TortoiseHaven.png/140px-Wc3TortoiseHaven.png",
	"tidewater glades lv": liqImg + "6/6c/Wc3TidewaterGlades_LV.png/140px-Wc3TidewaterGlades_LV.png",
	"tortoise haven":      liqImg + "6/60/Wc3TortoiseHaven.png/140px-Wc3TortoiseHaven.png",
	"turtle rock":         liqImg + "1/14/Turtle_Rock.png/140px-Turtle_Rock.png",
	"twisted meadows":     liqImg + "e/ee/Twisted_Meadows.png/140px-Twisted_Meadows.png",
}

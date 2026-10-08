# Garden Steward App — Phase 1 Plan

*Drafted 2026-09-30. What we are building and in what order. No platform or code decisions yet.*

## Purpose

The public Trail of the Saints page already serves pilgrims: stations, prayers, QR codes and a map. This app serves the **people who work the ground**. It helps a new volunteer know where they are, what they're looking at, and what to do with it. Pull or protect. Plant it here, not there.

Every feature serves both halves of the garden's mission: **devotion** (the saints' spaces) and **ecology** (natives, habitat, erosion, firewise).

## The Phase 1 test

> A new volunteer with no botany, holding only a phone, can find the rain garden, plant their tray in the right band, and come back a month later and weed it without pulling a single native.

If Phase 1 achieves that, it has done its job.

## Goals

1. **Name the garden.** Build a shared map of named places that everyone uses: volunteers, gardeners, and later the drone.
2. **Pull / Protect by place.** Volunteers can tell weeds from natives *in the place they're standing*, starting with the species actually on site.
3. **Plant the rain garden right.** Volunteers use the app's content to plant it in October 2026, and it looks established by Feb 6, 2027.
4. **Start the photo record.** Collect photos, each tied to a place, beginning with a fixed "before" photo point for every place.
5. **Leave room for Phases 2–3.** Design so individual plants, GPS, watering data and drone imagery can be added later without rework. Don't build any of them now.

## Who

| Role | Who | What they do |
|---|---|---|
| Volunteer | New helpers, no botany | Browse the map and cards, send photos, ask "What is this?" |
| Steward (admin) | The garden stewards | Edits places and species, checks IDs against sources, sets "today's job". Shown in the app as "the garden stewards", never by name |
| Future curator | A botanist or master naturalist, when one joins | Takes over ID confirmation |

Offer Spanish as well. The paid gardeners work in Spanish, and some volunteers may too. Content was to be written in English first; since 2026-10-07 a person writes in whichever language they think in, one box per field, and Claude translates it into the other (design.md, principle 6), so a gardener can add a note in Spanish as readily as a steward in English.

## What "the Garden" covers

Everything outdoors that the Fathers look after: beds, woods, trail, slopes, water, fire safety and wildlife. The app lives at **`stewards.schoenstatt-fathers.us`**. It's named for the people who use it, so the pilgrims keep `/trail/`.

## How the garden is described: independent layers

Places are the backbone. The other layers are **separate maps over the same ground**, not levels in a hierarchy.

| Layer | Describes | Phase 1 treatment |
|---|---|---|
| **Places** | Named areas people stand in and work in | Built out fully; the front door of the app |
| **Watering** | Rachio zones, drip lines, hand-watered, not watered | A label on each place card. One zone can cover many places, and a place can have several zones or none |
| **Conditions** | Sun, slope, soil, wet or dry | Notes on the place card |
| **Water flow and erosion** | Where rain goes | Phase 3 (terrain model) |
| **Terrain and grading** | Slopes, terraces, walls, erosion work | Items and notes on a place (for example, "leveled into two terraces, Oct 2026"); modeled in Phase 3 |
| **Fire safety** | The standard fire-safety zones measured out from the house, brush clearing, brush piles | A later layer; for now, a note on each place |
| **Wildlife** | What lives and feeds here | Observations on a place or a species ("monarchs on the frostweed"); trail cams later |

Rules:
- **A place can contain smaller places, only where it helps.** The rain garden is split into three bands. The switchbacks can stay one place.
- **Pull / protect belongs to species + place,** not to species alone. Poison ivy is native: it's removed along the paths but might be kept deep in the woods.
- **Choose the place on the map, not from GPS.** Phone GPS under canopy drifts 5–15 m. Keep the GPS a photo records anyway, for Phase 2, but never depend on it.
- **Species status is explicit.** The status list is native, native-derived cultivar or hybrid, adapted, edible or herb, and invasive, plus "not yet confirmed" for any ID. This leaves an honest record for whoever curates later. An ID's authority comes from its sources, not from a person.

## Species cards: one record, two jobs

The same species is looked at for two different reasons, so the card leads with different things depending on the job.

**Planting: "What will this look like here in a few years, through the seasons?"**
The planter is imagining a future garden, not identifying a plant. The card leads with:
1. **Flower color**
2. **Bloom season**, shown on a strip of the twelve months, not in words
3. **Mature size** (height × width), which also tells you spacing
4. **Shade tolerance**
5. **Two photos:** a **close-up** of the flower or leaf, and the **mature plant in context**, full size in a garden setting and not a nursery pot

When several plants are chosen for one place, the same data forms a **bloom calendar for the place**: which colors appear in which months, and where the gaps are. That's how you arrange a planting so something is always happening.

**Weeding: "Do I pull this?"**
The weeder is kneeling over a small plant. The card leads with:
1. **The seedling or young-plant photo**
2. **Look-alikes**, and how to tell them apart
3. **The action here:** pull, protect, or careful (wear PPE)

Both views share: English and Spanish names, scientific name, status (native, cultivar, and so on), and "not yet confirmed" where the ID is still open.

**Photos improve over time.** At first they come from wherever we can get them with permission. Over the seasons, our own photos of our own plants replace them. A card showing "our Winecup, rim of the rain garden, April 2027" beats any nursery photo, and it's the Phase 2 photo record growing for free.

## What's in and what's out

**In Phase 1**
- The place map: a simple drawn map, tap an area to open its card
- Place cards: photo, purpose, conditions, watering label, what's planted, pull list, protect list, "today's job"
- Species cards: one record per species, shown two ways, for **planting** and for **weeding** (see below)
- Photo inbox: who, when, which place, optional species, note, and a "What is this?" button
- Photo points: one fixed vantage per place, re-shot each season

**Deliberately later**
- A full task and maintenance system (the shed sheet and "today's job" cover it for now)
- Rachio integration and watering models
- Tracking individual plants (Phase 2). Leave room for it: a place card will later list its plants, and a photo can later point to one
- Drone, spectral imaging, trail cams, 3D terrain (Phase 3)

## The pilot: the rain garden

The planting is in the **weeks of Oct 5 and 12, 2026**. The app will not be built by then. **The content is the product.** It can be used on planting day as a printed guide and then become the first place card in the app.

### A proven model already exists: the skinny basement bed guide

`personal-tasks/skinny-basement-bed/`, planted 2026-09-22, is the species and place card in paper form, and it worked. Its pieces map directly onto Phase 1:

| In the skinny bed guide | In the app |
|---|---|
| `plants.json`: name, Latin name, size, flower color and swatch, bloom, light, water, note | The **species record** (planting view) |
| The "run" diagram: numbered blocks from the sunny end to the shade end | The **place layout**: what's where within a place |
| The planted, still-potted and leftover counts | A seed of Phase 2 plant tracking |
| Wikimedia photos, checked against the species and credited | The photo sourcing policy (it caught two wingstem photos passed off as frostweed) |
| The drip-line table, with three watering needs inside one bed | Proof that watering is its own layer, not a hierarchy level |

What the rain garden guide adds to that format: a **mature-in-context photo** next to the close-up, and a **bloom strip** so you can see the whole year across the plants.

### The site (photos 05 and 06, Sep 30)

- **Northeast corner of the house.** The skinny bed guide already records **4–5 hours of morning sun**. Treat it as **part shade**: bright, but not afternoon sun.
- **Its shape:** a level bed of fresh soil, held up by a curved dry-stacked limestone wall, above the gravel fire-pit patio.
- **Where the water comes in** (to confirm):
  - **Two white PVC outlets** at the back right, at the foot of the concrete wall. Downspouts?
  - **A gravel wash** coming down the slope at the back left, past the cedar log crib.
- **Where it goes out:** through and over the dry-stack wall, toward the patio.
- **Who sees it:** everyone sitting at the fire-pit benches. It's the backdrop of the gathering space. It is also where the trail begins at St. Joseph (per the trail map).

This changes the zones. It isn't a bowl with floor, slopes and rim. It's a level bed where the **water arrives at two points and leaves over the wall**:

| Zone | Where | Conditions | What it's for |
|---|---|---|---|
| **Inflow** | Around the pipe outlets and the foot of the gravel wash | Wettest; takes the force of the water | Deep roots to hold soil, plants that tolerate getting soaked |
| **Middle** | The body of the bed | Moist after rain, part shade | The tall backdrop and the fall color |
| **Wall edge** | Along the top of the dry-stack wall | Drains fastest, and the stone holds heat | Low plants that spill over the wall, kept below the benches' view line |

**Height rule, for devotion and gathering:** tall at the back against the concrete and the hill, low along the wall. Nothing should block the view from the benches, or of St. Joseph.

### Plants already in pots, earmarked for here

From the skinny bed guide:
- **Inland sea oats ×6.** A very good fit: shade, wet or dry, and it holds soil. Middle or inflow zone.
- **Frostweed ×2.** A very good fit: moisture and part shade, 4–7 ft, white fall bloom, and a monarch nectar plant. Back of the middle zone.
- **Autumn sage 'White' ×3 and Skeleton-leaf goldeneye ×1.** A poor fit. They want dry soil and full sun, and 4–5 hours of morning sun is the bottom of what autumn sage will flower on. Find them a sunnier spot elsewhere, or use the wall edge at its sunniest point only.

### Candidates from the Natural Gardener list (September 2026), sorted for part shade

| Fit | Plant | Color / bloom | Mature size | Zone |
|---|---|---|---|---|
| Good | Woodland Creek Sedge | green; spikes spring and fall | 1–2' × 1–2' | Inflow (wet or dry) |
| Good | Webberville Sedge | green groundcover | 1–3' × 8" | Inflow or middle; ground cover between the others |
| Good | Pigeonberry | pink, then red berries; May–Nov | 18–24" × 18" | Middle |
| Good | Brazos Penstemon | purple-pink, Mar–May; red winter leaves | 2' × 2' | Middle or inflow ("poor drainage OK") |
| Good | Missouri Violet | purple or white; Feb–Apr | 2–6" × 12" | Wall edge, front (the one that blooms by Feb 6) |
| Good | Gray Goldenrod | yellow; late summer to fall | 18–24" × 2' | Middle |
| Maybe | Fall Aster | purple; Sep–Nov | 3' × 30" | Middle, sunniest side |
| Maybe | Lanceleaf Coreopsis | yellow; Apr–Jun | 18–24" × 12" | Wall edge, sunniest side |
| Maybe | Snake Herb | purple-blue; Apr–Jul | 6–12" × 2' | Wall edge, sunniest side |
| Maybe | Wooly Stemodia | lavender; silver leaves | 6–10" × 3' | Wall edge; drapes over stone, but needs good drainage |
| Maybe | Clover Fern | no flowers | 6–10" × 18" | Only right at the inflow, if it stays wet |
| Poor | Winecup, Blue Grama, Salvia greggii, Rock Rose, Velvet Leaf Senna, Gregg's Mist Flower | all full-sun plants | | Better elsewhere |
| Not here | Comfrey, Sweet Woodruff, Pink Skullcap | listed as non-native | | Herb or kitchen place, if anywhere |

**Worth asking The Natural Gardener about** (part-shade natives that suit a rain garden and aren't on the list): **Turk's cap** (red, summer to fall, hummingbirds, handles wet and dry), **cedar sage** (red, spring, shade), **Texas columbine** (yellow, spring, shade), and **American beautyberry** (purple fall berries). I'm working from general knowledge on these, so check them against the Wildflower Center before buying.

### What it looks like through the year (good and maybe fits, plus the potted plants)

| Plant | J | F | M | A | M | J | J | A | S | O | N | D |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| Missouri Violet | | ● | ● | ● | | | | | | | | |
| Brazos Penstemon | | | ● | ● | ● | | | | | | | |
| Lanceleaf Coreopsis | | | | ● | ● | ● | | | | | | |
| Snake Herb | | | | ● | ● | ● | ● | | | | | |
| Pigeonberry | | | | | ● | ● | ● | ● | ● | ● | ● | |
| Inland Sea Oats (seed heads) | | | | | | ● | ● | ● | ● | ● | | |
| Gray Goldenrod | | | | | | | | ● | ● | ● | ● | |
| Frostweed | | | | | | | | | ● | ● | ● | |
| Fall Aster | | | | | | | | | ● | ● | ● | |

The months are approximate, read from nursery descriptions and the skinny bed data.

**Feb 6, 2027:** there will be little color. The violets may be starting, and the penstemon's red leaves and bronze sea oat seed heads will be there if they're left standing. The winter look will come from the stone wall, mulch, clean edges and evergreen sedges. Frostweed's winter trick (ice ribbons from its stems on a hard freeze) is a small bonus if the weather cooperates.

## Steps

### Now, this week (content, no app)
1. Walk the property and **name the places** (aim for 12–20). Sketch their boundaries on a printout of the trail map. Include the working areas the public map leaves out, such as the house beds, the parking wall and the fruit trees.
2. For each place, **choose a photo point** (a spot and a direction) and take the first photo. The four photos from Sep 30 are the start of this; see below.
3. **Rain garden site:** confirm the two water inlets: do the PVC outlets carry roof downspouts, and does the gravel wash run in a storm? Pick the photo point (photo 05, taken from the fire-pit bench, is a good one) and take the "before" photo of the bare soil.
4. List the **~15 species a volunteer will actually meet.** That's the rain garden plantings plus the known troublemakers: wild parsley (probably hedge parsley, with native look-alikes like chervil), tree-of-heaven, poison ivy, and whatever else comes up in the paths.

### Before planting (by about Oct 9)
5. Choose the rain garden plants and quantities. Start from the 8 already in pots (sea oats and frostweed) and the "good" fits, and ask The Natural Gardener about Turk's cap, cedar sage, columbine and beautyberry.
6. Make the **rain garden planting guide** in the skinny basement bed format: a layout of the zones (inflow, middle, wall edge), and for each plant its color, bloom months, mature size, light, a close-up photo and a mature-in-context photo, plus how to plant. Planters use it to imagine the space in a few years. This is the rain garden place card in paper form.
7. Make **protect cards** for every species being planted, showing what it looks like as a small plug in the ground.

### Planting days (weeks of Oct 5 and 12)
8. Volunteers plant from the sheet. Take photos during and after. Note what was confusing, because that's the design brief for the app.

### Design (runs alongside, from now)
9. **Rough mockups** of the four pilot screens: the map with the rain garden highlighted, the rain garden card with its zones, a species card, and the photo inbox or "What is this?".
10. Test the mockups by handing a phone to someone who knows nothing about the garden.

### Then, and only then
11. **Platform discussion:** hosting, offline use in the woods, sign-in, how the "What is this?" inbox reaches the stewards, and how this relates to the existing site.
12. Build the pilot: the map, the rain garden card, and the species cards for its plants. Then add places one at a time.

### Review (November, and again before Feb 6, 2027)
13. Re-shoot the rain garden photo point. What survived? What got pulled by mistake? Update the cards.

## Photos received 2026-09-30

Saved in `photos/inbox/`, which stays local: photos are data, not code, and are not in the repository. The places and IDs are unconfirmed, and they're a good first test of the inbox.

| File | What it shows | Place (to confirm) |
|---|---|---|
| `2026-09-30-01-wall-and-lawn.jpg` | Tall concrete retaining wall with vines on wires, a young green-barked tree, a dry lawn strip, and drip line along the wall | Parking retaining wall? Is this near the rain garden site? |
| `2026-09-30-02-bench-under-oaks.jpg` | Wooden bench under oaks, limestone boulders, a new tree in a guard tube, a mulched path | A station space; which saint? |
| `2026-09-30-03-slope-below-house.jpg` | Rocky slope below the house with new plantings, silver-leaved shrubs, drip line on the surface, cut stumps | West side plantings? |
| `2026-09-30-04-bench-under-rock.jpg` | Bench set under a limestone overhang in cedar and oak woods, with a fallen trunk across the approach | A station space; which saint? The fallen trunk is a work item |
| `2026-09-30-05-rain-garden-from-fire-pit.jpg` | The rain garden: a level soil bed behind a curved dry-stack limestone wall, with PVC outlets and a gravel wash at the back | **Rain garden.** A good photo point, from the fire-pit bench |
| `2026-09-30-06-fire-pit-patio.jpg` | The fire-pit patio: gravel, steel edging, four benches, and the rain garden wall behind | Fire pit (next to the rain garden) |

## Open questions

- Rain garden inlets: are the two PVC outlets downspouts? Does the gravel wash carry water in a storm?
- Is the rain garden bed dished at all, or level? A shallow dip at the inflow would hold water where the sedges want it.
- What's the volunteer's first contact? A work day with a steward present, or coming alone? This changes how much the app has to explain.
- Rain garden quantities and budget: is there a remaining budget at The Natural Gardener?
- Is there an existing list of the species "already on the property" (mentioned in the summer agreement)? It would seed the species cards.

# Garden Steward App — Design Document

*Drafted 2026-09-30. It describes how the app looks, reads and behaves. What it does is in `phase-1-plan.md`.*

**Home:** `stewards.schoenstatt-fathers.us`
**Brand authority:** *Brand Identity Manual — Schoenstatt Fathers* (11 Aug 2025).
**Machine-usable brand kit:** `/opt/projects/personal-tasks/brand/`. It contains `tokens.json`, `tokens.css`, the logo SVG and PNG files, and the Ubuntu and Inter fonts. Pull from there; don't re-derive from the PDF.

This is a Schoenstatt Fathers project, so it uses the Fathers' identity: the same logo, palette and type as the letterhead, the prayer boards and the public trail page. What's new here is the **field** setting: bright sun, dirty hands, no signal under the canopy, and a volunteer who has never been here. Where the brand manual is silent, this document decides. Where the manual speaks, it wins.

---

## 1. Where this sits

| | Public trail page | This app |
|---|---|---|
| URL | `schoenstatt-fathers.us/trail/` | `stewards.schoenstatt-fathers.us` |
| For | Pilgrims | Volunteers and stewards |
| Content | Stations, prayers, the map | Places, plants, work days, photos |
| Tone | Devotional | Practical, warm, plain |

**Rules carried over from the public page (`personal-tasks/trail-of-the-saints/web/README.md`):**
- **The QR anchors are a contract.** The anchors `#reinisch`, `#pozzobon`, `#francis`, `#therese` and `#joseph` on `/trail/` are burned into plywood. This app links *to* them. It never replaces them or moves them.
- **The boards are the authority for prayers.** If this app ever shows a station prayer, it's quoted from `signs.typ`, never retyped.
- **The map is north up.** It's a schematic, not a survey, and it uses the same trail geometry as `web/map.html`. The fire-pit entrance and anything that draws visitors toward the house belong only in this app. The public map leaves them out on purpose.

**Why `stewards.`:** the name says who the app is for, not which ground it covers. It doesn't compete with `/trail/` for pilgrims, and it doesn't shrink the scope to flower beds. The home screen still carries a small link, *"Walking the trail to pray? → Stations and prayers,"* for anyone who lands there by mistake.

**What "the Garden" means here:** everything outdoors that the Fathers look after in Austin. That's the planted beds, the woods and the trail, the slopes and the drainage, fire safety and irrigation, and the wildlife that lives there. It's the wide sense of the word (Eden, Gethsemane), not just flower beds. Grading and ecology aren't extra modules; they're layers over the same places (see `phase-1-plan.md`).

---

## 2. Brand elements

### Logo

| Use | Version (manual p. 8–9) | Where |
|---|---|---|
| App header, on white | Horizontal, full color | Top of the Map (home) screen |
| On the blue bar or the blue splash | "White + color" variant | Splash, sign-in, blue headers |
| App icon, favicon, home-screen icon | Isotype alone | PWA manifest, browser tab |
| Watermark | Isotype as oversized line art (p. 17, 21), about 6% opacity | Empty states only ("No photos yet") |

**Rules (manual pp. 12–14):**
- **Clear space** on every side equal to the width of the "S" in *Schoenstatt*.
- **Minimum size:** 25 mm for the horizontal lockup and 10 mm for the isotype. On screen, treat that as **at least 96 px wide** for the lockup and **at least 40 px** for the isotype.
- **Never** recolor, rotate, stretch, restructure, re-typeset, or add shadow.
- **Never** put the full-color logo on a photograph. On photos use the white (negative) or black (positive) version, which suits the garden photos.
- **Sion US:** the manual is the international one. Adapt the contact details, never the mark.

The trail and its stations are the Fathers' own devotional space, so the logo appears plainly. It isn't co-branded with the Movement of Austin. If a joint work day ever needs both, the manual's "monochrome non-institutional version for collaborations" (p. 13) is the tool for it.

### Color

The brand palette comes from `tokens.json`. It uses the colors **painted** in the manual's artwork, not the hex values typed on p. 4. The brand kit's README explains why.

| Token | Hex | Name | Role in this app |
|---|---|---|---|
| `--sf-blue` | `#293896` | Shrine | **Primary.** Header bar, headings, links, the trail line and station markers (as on the public map), "Today" banners |
| `--sf-blue-dk` | `#1E2A73` | (derived) | Pressed states, text on yellow |
| `--sf-yellow` | `#FFCB00` | Holy City | **Accent.** Eyebrow labels on blue, the one main action per screen ("What is this?", "Send"). Never text on white |
| `--sf-green` | `#38B54A` | Mount Sion | **Graphics only.** Place outlines, the Rosary Trail dash, bloom dots, map woods. **Never text or white-on-green** |
| `--sf-neutral` | `#F2F2F2` | Complementary | Page ground |
| `--sf-ink` | `#231F20` | Text black | Body text |
| `--sf-white` | `#FFFFFF` | | Cards and surfaces |

**Contrast, measured.** In full sun this matters more than usual.

| Pair | Ratio | Verdict |
|---|---|---|
| White on Shrine blue | ~10:1 | Excellent, use freely |
| Ink on white | ~16:1 | Body text |
| Shrine blue on white | ~10:1 | Headings, links |
| Blue-dark on yellow | ~8.4:1 | Main-action buttons |
| Yellow on Shrine blue | ~6.6:1 | Eyebrow labels on blue only |
| White on Mount Sion green | ~2.7:1 | **Fails.** Never do this |
| Yellow on white | ~1.5:1 | **Fails.** Never do this |

**Colors for garden meaning.** These aren't in the manual. They're derived for this app, and they're used only as fills and tints with dark text.

| Token | Hex | Meaning |
|---|---|---|
| `--tos-protect` | `#1F6B2C` on tint `#E3F3E5` | Protect, keep, native. A darkened Mount Sion |
| `--tos-pull` | `#A8481A` on tint `#F8E4D7` | Pull or remove |
| `--tos-water` | `#0E5E7E` on tint `#DDEEF4` | Watering and wet zones |
| `--tos-sun` | `#6B4E00` on tint `#FFF3C4` | Sun and shade. A darkened Holy City |

**Never color alone.** Protect and Pull always carry **an icon and the word**: a shield with "Protect", a down arrow with "Pull". About 1 in 12 men can't tell this green from this orange, and a wrong guess kills a plant.

**Flower colors are data, not UI.** The swatch dots on species cards show real bloom colors from `plants.json`. They never stand in for interface states. White-flowered plants get a thin outline so the dot shows.

### Type

| Role | Face | Weights | Sizes on a phone |
|---|---|---|---|
| Screen titles | **Ubuntu** Bold | 700 | 30–34 px |
| Section headings | Ubuntu Bold | 700 | 22 px |
| Eyebrow labels | Ubuntu Medium, uppercase, tracked +8% | 500 | 13 px |
| Body and UI | **Inter** | 400, 500, 700 | **17 px body**, 15 px secondary, 13 px captions (never smaller) |
| Buttons | Inter Bold | 700 | 16–17 px |

- These follow the manual (p. 6–7): Ubuntu for "titles, subtitles, short texts", Inter for "continuous texts". Both are on Google Fonts, and the brand kit has true static weights.
- The eyebrow style borrows the logo's "FATHERS" treatment: capitals with generous tracking, for "a sense of order and air" (p. 5).
- **17 px body** is deliberately larger than a desktop site. It's read at arm's length, in glare, sometimes with sunglasses.
- Any text drawn inside an SVG map follows the public map's rule: at least 24 viewBox units, so it never renders below about 11 px.

### Shape and icons

- **Radius:** 12–14 px on cards, 8–10 px on buttons and thumbnails, pill shapes on chips. This echoes the rounded terminals of the isotype and of Ubuntu.
- **Icons:** 2 px stroke, round caps and joins, on a 24 px grid. One family throughout. **No emoji.**
- **No gradients, glows or drop shadows** (the manual forbids effects on the logo, and the UI should match). The one exception is a faint shadow under the floating place label on the map, so it lifts off the drawing.

---

## 3. Field-first principles

1. **Sunlight first.** Light theme only in Phase 1, with a white card on a light-gray ground. Body text at 7:1 or better. Don't rely on thin rules or pale gray text.
2. **Thumbs and gloves.** Every target is **at least 48 px**. The main action sits in the bottom third of the screen. Nothing depends on hover.
3. **One job per screen.** The Map says *where*. A Place says *what's here and what to do*. A Species card says *what this is*. "What is this?" says *I'm not sure*.
4. **The place comes from the map, not GPS.** Under oak and cedar canopy, GPS drifts 5–15 m. The volunteer taps the place. The GPS is recorded quietly for later, and it's never shown as fact.
5. **Offline in the woods.** Places, species and today's job are readable with no signal. Photos queue and send when the phone reconnects, with a visible "Waiting to send (2)".
6. **English and Spanish,** switchable on every screen from the header. Spanish is written or checked by a native speaker, never left as a raw machine translation.
7. **Say "not sure".** Every identification carries its status: *checked* (with the sources it was checked against, such as the nursery tag or the Wildflower Center), *not yet checked*, or *unknown*. The default advice is: **"Not sure? Leave it and send a photo."**
8. **Devotion is present, but quiet.** Station spaces show the saint's name and petition ("St. Francis, for peace") and link to the prayer on `/trail/`. The app is for tending the space, not for praying in it, so it doesn't reproduce the devotional content.
9. **No personal names in the interface, and no claims of expertise.** The app speaks as *"we"* or *"the garden stewards"*. An ID is trusted because of its **sources**, not because of who made it. Nobody on the team is presented as the botanist. Individual names appear only where a person chose to add theirs: the photographer's credit on a photo, or a volunteer's first name on their own report.

---

## 4. Voice

- **Plain words.** "Pull", "Protect", "Plant here". Not "remove invasive specimen".
- **Second person, present tense, short.** "Tall against the wall, low along the stones."
- **Warm, not cute.** Volunteers are giving their Saturday. Thank them, and don't use exclamation marks.
- **Show whose words are whose,** as the public page does. Nursery descriptions, Wildflower Center facts and our own notes are labeled by source.
- **Numbers the way gardeners say them:** "2–3 ft", "Aug–Nov", "4–5 hours of morning sun".

---

## 5. Imagery

- **Our photos first.** Photo points (same spot, same direction, each season) are the backbone of the place cards. A photo is always dated and credited to its place and its photographer.
- **Ours from here, then ours from elsewhere, then borrowed.** A steward's photo from a park or a trail is still ours, and often the best one there is of a plant, but a volunteer reading the card is looking at this garden. So the card says where it was taken, *Our photo · Pedernales Falls State Park · May 2027* (or *not taken here* when nobody said), and a kind's photo from the property comes before it however much newer the park's is. On a photo's screen, *Somewhere else, off the property* is one of the places in its list, with the place's name beside it.
- **Every species photo is labelled with one of six kinds**, so each view of the card can pick the right picture:

  | Kind | Shown on | Why |
  |---|---|---|
  | **Young plant** (seedling) | Weeding | What you're looking at when deciding whether to pull. It often looks nothing like the adult |
  | **Leaf close-up** | Weeding, ID | Most plants spend most of the year out of bloom. Leaf shape, edges and hairs tell a native from its look-alike in March |
  | **Flower close-up** | Planting, bloom calendar | What a planter is choosing for; it fixes the color the bloom strip shows |
  | **Fruit or seed** | Planting, weeding | Berries, pods, winged seeds, seed heads. What feeds the birds, and what tells nandina by its red berries or tree of heaven by its papery seeds (added 2026-10-06; seed heads were filed under winter before) |
  | **Mature plant, full size** | Planting | "Will it look right here?": the size and shape in a few years, in a garden setting, not a nursery pot |
  | **In winter** | Planting, weeding | How many prairie plants look for half the year, and why they're left standing |

  Leaf and flower are separate on purpose: a combined slot gets filled with the flower, and the weeder is left with nothing to compare. Each photo also carries the **day it was taken** (or the month, when that's all anyone knows), whether the plant is **in flower** and **in fruit** in it, its **source and credit**, and whether it has been **checked against the species**. The plant's first and last days in flower and in fruit each year are read from these: the stewards see them all, and a volunteer's card shows the dates from checked photos. An unchecked photo isn't shown to volunteers. A missing kind shows as "No young-plant photo yet", which also gives the stewards their photo to-do list.
- **Look-alikes are a link between two species**, each shown with its own leaf photo side by side, not a photo kind.
- **Borrowed photos** come from Wikimedia Commons or iNaturalist under an open license, **checked against the species** before use, and credited on the card. The skinny bed guide's rule stays: two "frostweed" photos turned out to be wingstem.
- **The logo on photos** is always the white or black version (§2).
- **No stock photography** of generic gardens or people.

---

## 6. Screens and components

The phase 1 mockups are at <https://claude.ai/artifact/EZV9C92thqqWH5drvW2AQr>.

| Screen | Its job | Key components |
|---|---|---|
| **Map (home)** | Where are you working? | Logo header · the "Today" work-day banner (blue, yellow eyebrow) · schematic map with tappable places · list of places (the map's accessible twin) · bottom nav |
| **Place** | What's here, what do I do? | Photo point with date · condition chips (sun, water, purpose) · today's job, English and Spanish · zone layout · "Planned here" rows · bloom calendar · Protect and Pull panels |
| **Species** | What is it, and will it look right here? | Planting and Weeding tabs on one record · close-up and in-context photos · color, bloom strip, mature size against a person, light · weeding: seedling photo, look-alikes, action for this place |
| **A photo, closer** | Is that the hairy stem, or the smooth one? | Tapping a card's photo (a magnifying glass in its corner) opens it on a page of its own at the size it was sent, up to 4096 px · the large picture first, the full one over it when it arrives · the credit as on the card · *Pinch to zoom in*: the phone's own zoom, no script · *Open the photo by itself* for a laptop · back to the card's same view. The inbox's *Sort a photo* zooms the same way |
| **What is this?** | I'm not sure | Photo · place picker (from the map) · "I don't know / It's one of ours / Something to fix" · note · Send (yellow) · "Your photos" with ID status and the stewards' reply |

**The stewards' photo inbox** sits behind sign-in, and is the stewards' half of "What is this?". The same principles apply: sunlight, thumbs, and one job per screen.

| Screen | Its job | Key components |
|---|---|---|
| **Send photos** | Get the photos off the phone, now | Many photos at once · one choice of where, *On the property* (always the default), *At a nursery* or *Somewhere else* (a park, a trail, a friend's garden), each showing only its own field: the place here, the nursery from the register, or where else, written · optional one-line note for the whole batch · nothing asked per photo · while sending, the form is locked and says *Sending 3 of 15* over a progress bar, a photo to a request, each larger than 4096 px on its longer side shrunk to that on the phone first (upright, with the camera's date), trying again when the signal drops; the inbox when all are in, or what was not kept and why |
| **Photo inbox** | What is waiting to be sorted? | Waiting photos by the day they were taken, three across, each with its ID, and where the day's were taken · "Sort, starting with the newest" · tap several photos of one plant and *Sort the chosen photos together* (the count on the button): the plant is named on the first and comes chosen on the rest · *Not sure yet*, with each photo's question · the count on the stewards' front page is the only reminder |
| **Sort a photo** | What is this one? | One screen, no choice page first: the photo · where it is in the batch ("3 of 12") and *Skip* · the plant as one tap among the plants last sorted to, or from the whole list · what it shows as six buttons · *In flower* and *In fruit or seed* · *I'm sure: mark it checked*, which puts it on the card at once · *Save, next photo* held under the thumb · *Or:* just planted, nursery stock (a nursery photo opens on that one), not sure yet, discard (with a tick to confirm) · the next photo is already loaded when Save is pressed |
| **Photos to check** | Is it what it was sorted as? | Every unchecked photo, across every plant, one at a time · the photo beside the plant's checked photo of the same kind, which is what checking is · what it is said to be, and the plant's sources · *Yes, it shows ‹plant›* checks it and goes on, *Undo* until the next tap · *Change* opens the photo's own screen and comes back · *Skip* · the count on the stewards' front page |
| **Nurseries** | Where do we buy, and what should we know? | One card per nursery: name, address with *Open in maps*, website, *Call*, a note ("natives in the back greenhouse"), when it was last visited and a link to what it had · *Add a nursery*; a nursery is also added by writing a new name when a tag photo is sorted · removable only until it is first visited |
| **Nursery stock** | What can we buy for the next bed? | Each nursery's latest visit first, earlier ones folded away · each line: the tag photo (kept three months), the plant it was matched to with its native status, the name on the tag, pot · price · how many · *Correct* |

**Keep from the first mockups** (reviewed 2026-09-30): the **month-by-month bloom color map** on the place card, and the species card's **list of the places it grows** ("Where it grows here"). A species links to every place it's in, and each place links back.

**Shared components**

- **Header:** logo on the Map screen; on other screens a back link and the EN/ES toggle.
- **Bottom nav:** Map · Plants · **What is this?** The last one is the yellow action, always in reach.
- **Chip:** a tinted pill with an icon and a short fact ("Part shade · 4–5 h morning sun").
- **Place row:** photo-point thumbnail, name, one-line location, and a status tag ("Today", "Planted Sep 22").
- **Bloom strip:** twelve month cells with filled flower-color swatches, plus markers for key dates (for example, Feb 6).
- **Protect and Pull panels:** a pair, always side by side, always with icon and word.
- **Empty state:** the isotype watermark with one sentence and one action.
- **Plant picker:** wherever a steward chooses one of our plants from the whole list, a box with a magnifying glass rather than a long list. Any part of any word finds it, common, scientific or Spanish ("drum" finds the Turk's caps), the part typed marked on each match, the scientific name under the common one, and a tap chooses. Without its script it is the plain list it replaced.

---

## 7. Accessibility

- WCAG 2.2 AA at minimum. **AAA (7:1) for body text,** because of the sun.
- Real buttons, links and form labels. The map always has its list twin.
- Every photo has alt text naming the place and what's shown. Species photos name the species and the plant part.
- Respect "reduce motion". Nothing on the site needs animation anyway.
- Language is marked (`lang="es"`) on Spanish content, so screen readers switch voice.

---

## 8. Open design decisions

1. **Dark theme:** deferred. It isn't useful outdoors in daylight, but it might be for evening data entry.
2. **Vertical logo lockup** on the splash screen: it isn't extracted into the brand kit yet (manual p. 8).
3. **The typed-vs-painted hex values** on p. 4 are still worth raising with the manual's designer. This app follows the brand kit's choice.

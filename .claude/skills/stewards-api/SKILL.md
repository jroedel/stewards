---
name: stewards-api
description: Add plants, their photos and where they grow to the live garden steward app through its API, add or describe the garden's places, sort the steward's photo inbox, and read nursery stock for planning a bed, as the steward whose key is in STEWARDS_API_KEY. Use when the person asks to upload, import or add species data, species photos or a place's plants to the site, to sort, go through or identify the photos in their inbox, or to plan a bed from what the nurseries have — not for developing the app.
---

# Adding plants, photos and listings, and sorting the inbox, through the API

(Claude on claude.ai uses the same API as tools on `/mcp`, and is given the
same guidance from `app/domain/mcpapp/guide.md`. A rule changed here is
changed there too.)

The garden steward app has a JSON API at `/api/v1` for exactly this: a
steward's own Claude adding plants and photos without typing them into the
screens. This skill is how to use it well. It writes to the **live site**
that volunteers read, so it is used only when the person has asked for an
upload, never while developing the app (CLAUDE.md §6) and never to test a
change.

## Start from the index, every time

```sh
scripts/stewards-api GET /api/v1
```

The index lists every endpoint, the fields each takes, the values a field may
have, and the rules. It is built from the binary's own route table, so it is
always the version that is running; trust it over this file. Read it before
the first call in a session.

## The key

`scripts/stewards-api` reads the key from `STEWARDS_API_KEY`, or else from
`~/.config/stewards/api-key` (mode 600), and hands it to curl without putting
it on a command line. The person makes a key at `/steward/keys` and saves it
in that file themselves. The file is the way for Claude Code, whose shell
keeps no variables between commands. It is never `secrets.env`, which is not
to be read at all (CLAUDE.md §6).

- Never print it, echo it, write it to a file, or put its value in a command.
  Refer to it only as `$STEWARDS_API_KEY`, and only through the script.
- Never read the key file, or look for a key anywhere else. If the script
  says there is no key, or the API answers 401, stop and ask the person to
  make one or save it.

## What the API will not do, and why that is right

- **It never confirms a plant or checks a photo.** A steward does that on the
  screens after looking. Do not send `confirmed` or `checked`, and do not
  describe an upload as confirmed. The skinny bed guide's two "frostweed"
  photos were wingstem; a source saying a name is not the same as the plant
  being that species.
- **It deletes no plant, photo or place.** That is a person's job on the
  screens. It can take a plant off a place's list
  (`DELETE /api/v1/places/<place>/plants/<species>`), for one listed in the
  wrong place; the plant and its photos stay.
- **A pull is on the place card at once.** A listing has no box for a
  steward to tick first, and pull tells volunteers to take the plant out the
  next time they are there. The stewards decided the API may mark pulls; the
  care that asks for is yours. Mark one only for a plant the person has
  agreed is to come out *at that place* (pull or protect belongs to a plant
  and a place, not to the plant: poison ivy is native), and always say why in
  the note ("Invasive: take it out, root and all, before it seeds").
- **A photo's day and flower can be corrected** with the same PATCH:
  `taken_on` (`"2026-04-03"`, for a photo whose camera did not record it)
  and `in_flower`.
- **A change to a checked photo clears its check.** Correct a photo's kind,
  month or place with `PATCH /api/v1/photos/<id>`, sending only what changes.
  The answer says `"check_cleared": true` when it took a check away; tell
  the person it needs checking again.
- **A change to a confirmed plant clears its confirmation.** The answer says
  `"confirmation_cleared": true`. Before changing a confirmed plant, tell the
  person it will need confirming again.

## How to run a batch

1. **Read what is there:** `GET /api/v1/species` (and `/api/v1/places` for
   photo places). Match by scientific name as well as slug, so a plant that is
   already there under another address is not added twice.
2. **Draft locally and show the person** what you will send: a short table of
   plants (slug, common name, scientific name, what changes) and photos (plant,
   kind, source, credit). Wait for a yes. A batch to the live site is not
   something to start unasked.
3. **Send plants** with `PUT /api/v1/species/<slug>`, one JSON file each:

   ```sh
   scripts/stewards-api PUT /api/v1/species/winecup --json @batch/winecup.json
   ```

   The slug is permanent (it is printed on stakes): lower-case, hyphens,
   the common name, such as `winecup` or `brazos-penstemon`. Sizes are whole
   inches; bloom months are 1–12.
4. **Send photos** with `POST /api/v1/species/<slug>/photos`:

   ```sh
   scripts/stewards-api POST /api/v1/species/winecup/photos \
     -F photo=@batch/winecup-leaf.jpg -F kind=leaf -F source=borrowed \
     -F 'credit=A. Botanist' -F 'license=CC BY-SA 4.0' \
     -F 'source_url=https://commons.wikimedia.org/wiki/File:…'
   ```

5. **List plants at places** with
   `PUT /api/v1/places/<place>/plants/<species>`, once the plant exists:

   ```sh
   scripts/stewards-api PUT /api/v1/places/skinny-basement-bed/plants/pigeonberry \
     --json '{"action": "protect", "planned": true, "note": {"en": "6 plants, at the shady end"}}'
   ```

   `planned` is true when there is planting still to do: none of it is in
   the ground yet, or more is going in beside what is there. It puts the
   plant on the place's To plant list. It is false for anything already
   growing, whether planted last week or come up on its own. Ask the person
   which it is rather than guess: a bed planted a month ago is not "to
   plant". There is no count or zone field: put them in the note, and for
   "more going in" say how many ("6 in, 3 more by the wall").
   `GET /api/v1/places/<place>/plants` reads back what a place lists.
6. **Report** what was created, updated, unchanged and duplicate, and give the
   person the `photos_url` of each plant: that screen is where they check the
   photos, one by one, before any volunteer sees them.

Sending the same batch again is safe: an unchanged plant is `unchanged`, and a
photo already kept answers `"duplicate": true`.

## Plant data

- `sources` is what the ID was checked against. The Lady Bird Johnson
  Wildflower Center's page for the species is the usual first one; give its
  URL. Do not cite a source you did not read.
- `status`: `native`, `cultivar` (a native-derived cultivar or hybrid),
  `adapted`, `edible`, `invasive`.
- English only in `en`. Leave every `es` out: Spanish comes from a native
  speaker, never from machine translation (design.md).
- Notes are for someone standing in the sun with dirty hands: short, plain,
  practical.

## Places

Places are the named areas people stand in and work in: the rain garden,
its three bands, the switchbacks. `GET /api/v1/places` gives each as its
card describes it -- `purpose`, `conditions`, `photo_point`, its `parent`,
its `spot` on the map -- so read it before saying anything about a place.

- **Add or change one** with `PUT /api/v1/places/<slug>`, sending the whole
  place: a field left out is emptied, so read it first and send it back with
  your change. Do it only when the person has described the place or asked
  for the change; a place is theirs to name.
- **The slug is forever.** It is the card's address, printed on stakes and
  QR boards. Choose a short, plain one (`skinny-bed`, `inflow`) and ask the
  person before creating a place whose slug you made up.
- **A smaller place inside another** (a band of the rain garden) names it as
  `parent`. Only where it helps: the switchbacks can stay one place.
- **The map** is a drawing of the property, not a survey. Put a place on it
  with `PUT /api/v1/places/<slug>/spot` (`{"x", "y"}` in the drawing's
  units, which the list's `map` gives) only when the person has said where it
  is relative to places already on it, and tell them to check it on the map
  screen. A place inside another is never on the map.
- English only, as for plants.
- Removing a place is the person's, on its screen.

## Photos

Each photo is one of six kinds, and the kind is what the card shows it as:
`young` (the seedling — what a weeder is deciding about), `leaf` (a close-up,
for telling it from a look-alike out of bloom), `flower` (a close-up),
`fruit` (fruit or seed: berries, pods, winged seeds, seed heads), `mature`
(the grown plant at full size, in a garden, not a nursery pot) and `winter`
(how it looks in winter). Seed heads were `winter` before fruit came;
re-file one with `PATCH /api/v1/photos/<id>` only when the person asks. Leaf and flower are
separate on purpose; a photo showing both is the one that shows the leaf
better.

- **Borrowed** photos come from iNaturalist or Wikimedia Commons. The
  stewards have decided CC0, CC BY, CC BY-SA and **CC BY-NC** are all fine
  (the site is a non-commercial ministry, which is the use BY-NC allows).
  Never ND, and never all-rights-reserved. Take the author, licence and the
  photo's page from the source's API, not from memory: on iNaturalist the
  photo's `license_code` and the observer's name, with
  `https://www.inaturalist.org/photos/<id>` as its page; on Commons
  `prop=imageinfo&iiprop=url|extmetadata` (`Artist`, `LicenseShortName`,
  `descriptionurl`).
- **Cultivars** get their own plant record when they look different from the
  species (a white autumn sage, a pink Turk's cap), with photos of that
  cultivar. A cultivar that looks like the species shares its record, and is
  named in the note.
- **Our own** photos are `source=ours`, with `place` as a slug from
  `/api/v1/places` when the person says where it was taken. One of ours
  taken off the property, in a park or on a trail, has `taken_where` with
  the spot's name instead (or `elsewhere=true` when nobody said where), and
  never a `place`: the card says where it was taken, so nobody takes it for
  a plant in this garden, and shows a photo taken here before it.
- **Never upload a photo in which a person can be recognised.** Volunteers are
  in some of the garden's own photos (CLAUDE.md, "Photos and people"); leave
  those for the person to decide about on the screen.
- Download borrowed photos into `photos/` (which is gitignored) before
  uploading, and keep the originals as the camera or the source saved them:
  the server makes the sizes it needs and strips location and camera details
  itself.

## Sorting the photo inbox

The steward sends photos from the garden, a nursery or anywhere else to the
inbox in batches, and sorts them later. "Sort my inbox" means this:

1. **List what is waiting:** `GET /api/v1/inbox` (and `?status=unsure` for
   the photos set aside earlier). Each photo says `at`: `property` (with the
   `place` if the steward chose one), `nursery` or `elsewhere` (a park, a
   trail, a friend's garden), with `where` naming the nursery or the spot if
   the steward said; a `note`; and when the camera says it was taken. A photo
   from elsewhere can only be a `photo` of its plant (or set aside): never
   `planted`, never `stock`, and never given a `place`. The plant's photo
   keeps the batch's `where` as its `taken_where`.
2. **Look at each photo.** Download its `large_url` into `photos/inbox/`
   (gitignored), named by its id, and read it:

   ```sh
   scripts/stewards-api GET /api/v1/inbox/<id>/large.jpg -o photos/inbox/<id>.jpg
   ```

   Group them by day and place: ten photos taken a minute apart at the
   inflow band are one walk, and their notes and neighbours are evidence.

   When the large one cannot settle it -- a grass's seed head, the hairs on
   a stem, a leaf's edge -- download its `full_url` as well: the photo as it
   was sent, up to 4096 pixels on its long side. A picture is read at about
   the large one's size whatever its own, so crop the part in question
   before reading it:

   ```sh
   scripts/stewards-api GET /api/v1/inbox/<id>/full.jpg -o photos/inbox/<id>-full.jpg
   convert photos/inbox/<id>-full.jpg -crop 1200x1200+1800+900 +repage photos/inbox/<id>-crop.jpg
   ```

   A plant's photo has a `full_url` too, once it has been sorted.
3. **Propose, in one table, and wait for a yes.** Name each photo by the
   first 8 characters of its id (`cbb9fac4`): the steward's screens show
   every photo as "Photo cbb9fac4", inbox and plant photos alike, so that is
   how the person finds the one you mean. Never a number of your own ("photo
   3"), which nothing on the screen matches. For each photo: what you
   think it shows and *why* (leaf shape, flower, habit — what you can see),
   how sure you are, the outcome, the plant's slug, the kind, the place. Your
   reading of a photo is a suggestion for the steward to agree to, never an
   identification: name the look-alikes when there are any (frostweed and
   wingstem, again), and say "not sure" freely. The outcomes are:
   - `photo` — a photo of the plant, for its photos: something to find out
     about, or a flower not often seen. Choose the `kind`, and send
     `"in_flower": true` whenever the plant is in bloom in it, and
     `"in_fruit": true` whenever it carries fruit or seed, whatever the
     kind (a flower or fruit close-up always is). The steward keeps a **flowering
     record** -- each plant's first and last day in flower and in fruit
     every year, and its first sighting -- from these and the day the camera recorded, so
     every plant photo is worth sorting, not only the best: say in your
     table which ones are in flower. `GET /api/v1/species/<slug>/flowering`
     reads the record back, for questions like "when did the winecup start
     flowering this year?" or "what is new on the property this year?"
   - `stock` — a plant for sale, from a nursery photo: see below.
   - `planted` — the plant was just planted at the `place`. It is listed
     there to protect and taken off the To plant list; the photo becomes its
     young plant. Only for photos on the property. Ask the person if it is a
     planting or just a photo when you cannot tell; a photo of a plant is not
     a planting.
   - `unsure` — set aside with a question in `note`. Use it for anything you
     cannot name with confidence, and for a photo you think should go
     (blurred, a pocket, a duplicate angle), with the reason: the API never
     discards, and the steward decides on the photo's `screen_url`.
4. **Add any plant that is not there yet first**, as in "How to run a batch",
   with sources you actually read. A plant added this way is not confirmed.
5. **Sort**, one photo at a time, once the person has said yes:

   ```sh
   scripts/stewards-api POST /api/v1/inbox/<id>/sort \
     --json '{"outcome": "photo", "species": "winecup", "kind": "flower"}'
   ```

   Sending the same sort twice answers `"unchanged": true`; a `409` means
   somebody sorted it differently meanwhile, and the answer says how.
6. **Report** what each photo became, and give the `photos_url` of each
   plant that got one: the steward checks them there before any volunteer
   sees them. List the photos set aside with their questions.

Nursery photos (`"at": "nursery"`) are stock for planning a bed. Sort each
as `stock`: `nursery` (a name from the register, `GET /api/v1/nurseries`),
`name_on_tag` as the tag writes it, and `pot_size`, `price` and `count` when
the photo shows them. A name not in the register is added to it as written,
so ask the person which nursery it was rather than guess one: a misspelling
becomes a second nursery for them to tidy up. The register itself -- address,
website, phone, a note -- is the steward's to fill in on the screens. Give `species` only when the tag's
scientific name matches a plant that is already here; a tag is evidence, not
an identification, and an unmatched line is fine. A good flower close-up from
a nursery can be a `photo` of the plant instead, if the person wants it.

A photo with a person who can be recognised in it is never sorted to a
plant's photos: set it aside and say so.

## Planning a bed from nursery stock

`GET /api/v1/nursery` is every visit, the most recent first, and `latest`
marks each nursery's most recent visit: what is on its tables now. To turn
it into a bed's candidate list:

1. Read the bed: its place card, conditions and what is already listed there
   (`GET /api/v1/places/<slug>/plants`), and the planting guide format the
   stewards use (color, bloom, size, light).
2. Take the `latest` visits' lines. For a line with a `species`, read the
   plant (`GET /api/v1/species/<slug>`) for its status, light, water, size
   and bloom. A line without one is a tag you can still reason about, but say
   it is unmatched.
3. Show the person a table: what fits the bed's light and water, natives
   first, what blooms when the bed has gaps, pot size and price, and how many
   the nursery had. Say what you left out and why.

A line read wrongly is corrected with `PUT /api/v1/nursery/lines/<id>`,
sending the whole line.

## When something is refused

Every refusal is `{"error": {"field": "…", "problem": "…"}}`, and the problem
is a sentence saying what to fix. Fix that field and send again. A refusal you
cannot fix from the sentence is worth stopping for and telling the person
about, rather than trying variations.

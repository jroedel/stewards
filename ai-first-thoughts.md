Designing this app requires bridging a spiritual experience with rigorous ecological management. If the ultimate goal is an AI-driven, drone-monitored ecological model, the Phase 1 foundation must be built on a highly structured data ontology, even while keeping the immediate volunteer experience incredibly simple.

Here is how to frame the design principles and feature sets before handing this off to a development agent.

## Design Principles

* **The Dual Mandate (Devotion + Ecology):** Every feature should serve the Trail of the Saints and the ecosystem simultaneously. A planting choice isn't just about erosion control; it’s about curating the contemplative space around a specific saint’s station.
* **Field-First UX:** Volunteers will be using this outside in the glaring Central Texas sun, likely with dirty hands. The UI must feature high-contrast modes, massive tap targets, and binary decision trees. It shouldn't look like a desktop dashboard crammed onto a phone.
* **Microclimate Awareness:** The app must natively understand that the garden is not a single entity. It is a collection of microclimates (shade under oaks, runoff slopes, exposed limestone) that dictate the "right plant, right spot" philosophy.

## Feature Planning: Phase 1 (Volunteer & Maintenance Core)

To integrate volunteers effectively, the app needs to remove the friction of botanical uncertainty and organize maintenance into actionable bites.

* **The "Pull or Protect" Engine:** Volunteers struggle most with identifying weeds versus native seedlings. The app needs a highly visual, localized field guide. Instead of an encyclopedia, volunteers need a geofenced binary view: "If you are standing in Zone 3, you are looking for *this* invasive weed to pull, and *this* native seedling to protect."
* **Maintenance Task Feed:** A master maintenance plan is useless to a weekend volunteer. The app should translate seasonal plans into a localized feed (e.g., "Spread mulch at Station 4," "Check irrigation emitters in Zone 2").
* **The "Right Spot" Matrix:** A feature that allows admins to log a location's variables (sun exposure, soil depth, slope, Rachio zone) and cross-reference them against a curated database of native species to generate planting recommendations.
* **Rachio Integration Dashboard:** A visual overlay mapping physical garden spaces to Rachio watering zones, allowing you to track irrigation frequency against observed plant health.

## The Data Ontology: Preparing for Phases 2 & 3

You don't need to build the AI or the drone paths yet, but Phase 1 will fail if its database can't support them later. You must tell your agent to structure the data specifically for future IoT and spatial integration.

* **Species vs. Instance:** The database must separate a *Plant Species* (e.g., Turk's Cap) from a *Plant Instance* (e.g., Turk's Cap #42, planted Oct 2025 at coordinate X,Y). Phase 2's GPS tracking and Phase 3's drone spectral imaging will need a specific "Instance" ID to attach their data to, otherwise, you'll never be able to analyze individual survival strategies.
* **Spatial Hierarchy:** A drone doesn't know what a Rachio zone is. The data model needs a strict hierarchy: `Garden -> Trail Segment -> Rachio Zone -> Microclimate -> Plant Instance`. This ensures that when the drone reports erosion, the app knows exactly which watering zone is causing it.

## The Pre-Flight Agent Briefing

Before asking an agent to start generating code, hand them a brief that explicitly defines:

1. **User Roles:** Strictly define what a "Botanist/Admin" can do (curate the native database, map Rachio zones, set maintenance logic) versus a "Volunteer" (consume tasks, upload field photos, report issues).
2. **The Core Entity Relationship (ER) Model:** Dictate the Species vs. Instance structure upfront so they don't build a flat, unscalable database.
3. **The Delivery Method:** Decide if this is a Progressive Web App (PWA) for offline capabilities (crucial if garden Wi-Fi/cellular is spotty) or a standard responsive web app.

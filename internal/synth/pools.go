package synth

// Word pools ported from db-anonymizer2 (internal/anonymizer and
// internal/dummyfiles). All values are invented; none name real people or
// firms. They are maintained here from now on.
var (
	// First names: common Anglo-Australian names plus names from Australia's main migrant communities.
	firstNames = []string{
		"James", "Mary", "Robert", "Patricia", "John", "Jennifer", "Michael", "Linda",
		"David", "Elizabeth", "William", "Barbara", "Richard", "Susan", "Joseph", "Jessica",
		"Thomas", "Sarah", "Charles", "Karen", "Christopher", "Lisa", "Daniel", "Nancy",
		"Matthew", "Betty", "Anthony", "Margaret", "Mark", "Sandra", "Donald", "Ashley",
		"Steven", "Dorothy", "Paul", "Kimberly", "Andrew", "Emily", "Joshua", "Donna",
		"Kenneth", "Michelle", "Kevin", "Carol", "Brian", "Amanda", "George", "Melissa",
		"Timothy", "Deborah", "Ronald", "Stephanie", "Edward", "Rebecca", "Jason", "Sharon",
		"Jeffrey", "Laura", "Ryan", "Cynthia", "Jacob", "Kathleen", "Gary", "Amy",
		"Liam", "Olivia", "Noah", "Charlotte", "Oliver", "Amelia", "Jack", "Isla",
		"Henry", "Mia", "Leo", "Ava", "Lucas", "Grace", "Hudson", "Chloe",
		"Archie", "Sophie", "Harrison", "Zoe", "Cooper", "Ruby", "Lachlan", "Matilda",
		"Angus", "Harper", "Hamish", "Ella", "Callum", "Evie", "Riley", "Willow",
		"Fraser", "Isabella", "Declan", "Hannah", "Bradley", "Georgia", "Craig", "Kylie",
		"Darren", "Tracey", "Shane", "Leanne", "Wayne", "Natalie", "Glenn", "Belinda",
		"Trent", "Megan", "Luke", "Jasmine", "Nathan", "Caitlin", "Dylan", "Brooke",
		"Wei", "Mei", "Jun", "Ling", "Hiroshi", "Yuki", "Minh", "Linh",
		"Raj", "Priya", "Arjun", "Ananya", "Sanjay", "Deepa", "Ahmed", "Fatima",
		"Omar", "Leila", "Giorgos", "Eleni", "Marco", "Giulia", "Luca", "Francesca",
		"Mateo", "Lucia", "Diego", "Sofia", "Tomasz", "Katarzyna", "Lars", "Ingrid",
		"Sean", "Siobhan", "Connor", "Aoife", "Tane", "Aroha", "Mohammed", "Aisha",
	}

	// Last names, mixed the same way as firstNames.
	lastNames = []string{
		"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis",
		"Rodriguez", "Martinez", "Hernandez", "Lopez", "Gonzalez", "Wilson", "Anderson",
		"Thomas", "Taylor", "Moore", "Jackson", "Martin", "Lee", "Perez", "Thompson",
		"White", "Harris", "Sanchez", "Clark", "Ramirez", "Lewis", "Robinson", "Walker",
		"Young", "Allen", "King", "Wright", "Scott", "Torres", "Nguyen", "Hill", "Flores",
		"Green", "Adams", "Nelson", "Baker", "Hall", "Rivera", "Campbell", "Mitchell",
		"Carter", "Roberts", "Gomez", "Phillips", "Evans", "Turner", "Diaz", "Parker",
		"Cruz", "Edwards", "Collins", "Reyes", "Stewart", "Morris", "Morales", "Murphy",
		"Kelly", "Ryan", "Walsh", "Byrne", "O'Brien", "O'Connor", "McDonald", "MacKenzie",
		"Fraser", "Henderson", "Ross", "Sutherland", "Cameron", "Grant", "Hughes", "Price",
		"Bennett", "Wood", "Barnes", "Fisher", "Hunter", "Graham", "Wallace", "Gibson",
		"Watson", "Brooks", "Kennedy", "Sullivan", "Burke", "Doyle", "Quinn", "Lynch",
		"Chen", "Wang", "Li", "Zhang", "Liu", "Huang", "Wong", "Tan",
		"Tran", "Pham", "Le", "Vo", "Patel", "Singh", "Sharma", "Kumar",
		"Gupta", "Khan", "Ali", "Hassan", "Rahman", "Nakamura", "Tanaka", "Sato",
		"Kim", "Park", "Choi", "Rossi", "Russo", "Romano", "Esposito", "Ricci",
		"Papadopoulos", "Georgiou", "Nikolaidis", "Kowalski", "Nowak", "Schmidt", "Muller", "Fischer",
		"Becker", "Hoffmann", "Jansen", "De Vries", "Van Dijk", "Larsen", "Nielsen", "Andersson",
		"Silva", "Santos", "Pereira", "Costa", "Fernandes", "Novak", "Horvat", "Ivanov",
		"Petrov", "Haddad", "Nasser", "Mansour", "Aziz", "Te Rangi", "Ngata", "Walker-Ross",
	}

	// First words of company names.
	companyStems = []string{
		"Abbott", "Ashford", "Banksia", "Barrington", "Bellamy", "Blackwood", "Bluegum", "Bramble",
		"Brennan", "Calloway", "Carrington", "Castlereagh", "Coastal", "Coolibah", "Corbett", "Crestwood",
		"Dalton", "Davenport", "Eastgate", "Ellison", "Eucalypt", "Fairbairn", "Fenwick", "Fitzroy",
		"Galloway", "Garrison", "Goldfield", "Granite", "Hadley", "Harbourside", "Harlow", "Hawthorn",
		"Highland", "Ironbark", "Jarrah", "Kestrel", "Kingsford", "Lachlan", "Lakeside", "Langford",
		"Lindqvist", "Macquarie", "Maddox", "Mallee", "Marlowe", "Meridian", "Merriweather", "Monaro",
		"Northcote", "Oakridge", "Osborne", "Pemberton", "Pinnacle", "Prescott", "Quinlan", "Redgum",
		"Riverbend", "Rosewood", "Saltbush", "Sandstone", "Sherwood", "Silverton", "Southern Cross", "Spinifex",
		"Stirling", "Summit", "Tasman", "Thornbury", "Torrens", "Trident", "Unity", "Vantage",
		"Wattle", "Westbrook", "Whitfield", "Willoughby", "Wollemi", "Yarra", "Yellowbox", "Zenith",
	}

	// Australian towns and regions, also used as company first words.
	companyPlaces = []string{
		"Albany", "Albury", "Alice Springs", "Ararat", "Armidale", "Ballarat", "Ballina", "Bairnsdale",
		"Bathurst", "Bega", "Bendigo", "Bowral", "Bunbury", "Bundaberg", "Busselton", "Byron",
		"Cairns", "Camden", "Capricorn", "Casino", "Cessnock", "Coffs Coast", "Cooma", "Cowra",
		"Dandenong", "Darling Downs", "Devonport", "Dubbo", "Echuca", "Esperance", "Eyre", "Geelong",
		"Geraldton", "Gippsland", "Gladstone", "Glenelg", "Gold Coast", "Goulburn", "Grafton", "Griffith",
		"Gympie", "Hawkesbury", "Hervey Bay", "Hobart", "Horsham", "Huon", "Illawarra", "Ipswich",
		"Kalgoorlie", "Katherine", "Kiama", "Launceston", "Lismore", "Lithgow", "Mackay", "Maitland",
		"Mandurah", "Margaret River", "Mildura", "Moama", "Moree", "Mudgee", "Murray", "Newcastle",
		"Noosa", "Orange", "Parkes", "Pilbara", "Port Hedland", "Portland", "Riverina", "Rockhampton",
		"Shepparton", "Sunraysia", "Tamworth", "Toowoomba", "Townsville", "Wagga", "Warrnambool", "Whitsunday",
	}

	// The trade in a company name; also its industry.
	companyTrades = []string{
		"Civil Engineering", "Logistics", "Transport", "Constructions", "Plumbing", "Electrical",
		"Earthmoving", "Landscaping", "Hospitality", "Property", "Developments", "Manufacturing",
		"Fabrication", "Engineering", "Consulting", "Accounting", "Legal", "Medical",
		"Dental", "Veterinary", "Childcare", "Aged Care", "Freight", "Marine",
		"Aviation", "Agriculture", "Viticulture", "Farming", "Timber", "Steel",
		"Roofing", "Glazing", "Joinery", "Painting", "Cleaning", "Security",
		"Retail", "Wholesale", "Imports", "Motors", "Auto Repairs", "Mining",
		"Energy", "Solar", "Technology", "Software", "Media", "Events",
	}

	// Company name endings.
	companySuffixes = []string{
		"Pty Ltd", "Group", "Holdings", "Services", "Partners", "Australia", "& Co", "Co Pty Ltd",
	}

	// Insurance: noun phrases that start a sentence.
	insSubjects = []string{
		"The claim", "The policy", "The renewal", "The endorsement", "The excess",
		"The premium adjustment", "The certificate of currency", "The insured",
		"The claimant", "The broker", "The underwriter", "The loss assessor",
		"The third-party assessment", "The property damage claim",
		"The public liability cover", "The professional indemnity policy",
		"The business interruption claim", "The motor fleet schedule",
		"The workers compensation policy", "The cyber liability cover",
		"The management liability quote", "The marine cargo declaration",
		"The strata insurance renewal", "The claims history report",
		"The proposal form", "The risk assessment", "The settlement offer",
		"The recovery action", "The reinsurance treaty", "The binder agreement",
	}

	// Insurance: predicates completing a sentence.
	insPredicates = []string{
		"has been lodged with the insurer for review",
		"is pending the loss assessor's report",
		"has been referred to the underwriter for further consideration",
		"requires an updated schedule before it can be finalised",
		"was approved subject to the standard policy excess",
		"has been renewed for a further twelve-month period",
		"is awaiting confirmation of the sum insured",
		"has been settled in accordance with the policy terms",
		"was declined as it falls outside the scope of cover",
		"needs the certificate of currency reissued to the mortgagee",
		"has been escalated to the claims manager for approval",
		"remains open pending receipt of the supporting documentation",
		"was adjusted following the mid-term review of the risk",
		"has been forwarded to the reinsurer for their records",
		"is being handled under the broker's delegated authority",
		"requires the client's signed proposal form to proceed",
		"has been noted on the policy as an endorsement",
		"will be reviewed at the next scheduled renewal",
		"was assessed against the applicable sub-limits and deductibles",
		"has been cross-referenced with the prior claims history",
	}

	// Insurance: trailing sentences for length and variety.
	insContexts = []string{
		"Please ensure the client is kept informed of any changes to the cover.",
		"The premium remains payable within fourteen days of the invoice date.",
		"Any variation to the sum insured should be confirmed in writing.",
		"The excess applicable to this section is detailed in the policy schedule.",
		"Supporting documentation has been saved against the claim file.",
		"The insurer has requested additional information before proceeding.",
		"This matter has been diarised for follow-up next week.",
		"Confirmation of cover will be issued once the premium is receipted.",
		"The assessor will make contact to arrange a site inspection.",
		"All correspondence has been logged against the policy record.",
		"The broker will review the terms ahead of the renewal date.",
		"Kindly review and advise if the proposed terms are acceptable.",
		"The claim reference has been provided for future correspondence.",
		"A revised schedule will follow under separate cover.",
		"Please retain this notice for your records.",
	}

	// Insurance: email openers (a comma follows).
	insOpeners = []string{
		"Further to our recent discussion",
		"Thank you for your enquiry regarding the policy",
		"Following the review of your account",
		"As requested",
		"With reference to the claim lodged",
		"In relation to the upcoming renewal",
		"Please find below an update on the matter",
		"Following receipt of your proposal form",
		"After liaising with the underwriter",
		"Ahead of the policy renewal",
	}

	// Insurance: email closing sentences.
	insClosers = []string{
		"Please do not hesitate to contact our office should you have any questions.",
		"We will be in touch once we hear back from the insurer.",
		"Thank you for your continued business.",
		"We appreciate the opportunity to assist with your insurance requirements.",
		"Please confirm your instructions at your earliest convenience.",
		"We look forward to finalising the cover on your behalf.",
	}

	// Insurance: document title nouns.
	insTitles = []string{
		"Policy Schedule", "Claim Summary", "Renewal Notice", "Certificate of Currency",
		"Risk Assessment Report", "Insurance Proposal", "Premium Funding Agreement",
		"Loss Adjuster Report", "Cover Note", "Endorsement Advice", "Tax Invoice",
		"Statement of Advice", "Claims History Report", "Underwriting Submission",
	}

	// Insurance: document title qualifiers.
	insQualifiers = []string{
		"Commercial Property", "Public Liability", "Professional Indemnity",
		"Business Pack", "Motor Fleet", "Workers Compensation", "Marine Cargo",
		"Management Liability", "Cyber Protection", "Strata", "Farm Pack",
		"Construction", "Trade Package",
	}

	// Insurance: short labels for subjects and line items.
	insLabels = []string{
		"Policy Renewal", "Claim Assessment", "Premium Adjustment", "Cover Note",
		"Renewal Reminder", "New Business Quote", "Mid-Term Adjustment",
		"Certificate Request", "Endorsement Advice", "Claim Lodgement",
		"Loss Assessment", "Underwriting Review", "Policy Cancellation",
		"Premium Funding", "Claims Follow-Up", "Risk Review", "Cover Confirmation",
		"Broker Follow-Up", "Settlement Advice", "Recovery Action",
		"Proposal Received", "Schedule Update", "Excess Waiver Request",
		"Compliance Check", "Client Onboarding", "Account Review",
		"Quote Comparison", "Insurer Response", "Documentation Request",
		"Payment Reminder", "Policy Amendment", "Claim Settled",
		"Cover Declined", "Awaiting Assessor", "Referred to Underwriter",
		"Public Liability", "Professional Indemnity", "Business Interruption",
		"Property Damage", "Motor Fleet", "Workers Compensation",
		"Cyber Liability", "Management Liability", "Marine Cargo", "Strata Renewal",
	}

	// Manufacturing: noun phrases that start a sentence.
	mfgSubjects = []string{
		"The steel fabrication job", "The laser-cut plate batch", "The welded frame assembly",
		"The stainless steel brackets", "The aluminium panel order", "The CNC machining job",
		"The sheet metal enclosure", "The structural steel package", "The pipe spool assembly",
		"The press-brake forming job", "The metalwork production run", "The fabricated handrail",
		"The workshop cutting list", "The weld inspection record", "The powder-coated assembly",
	}

	// Manufacturing: predicates completing a sentence.
	mfgPredicates = []string{
		"is ready for the next workshop operation",
		"has been checked against the approved fabrication drawings",
		"is awaiting material delivery before production can start",
		"requires a dimensional check before final assembly",
		"has been scheduled for cutting and welding this week",
		"is ready for fit-up inspection on the fabrication bench",
		"needs confirmation of the material grade and plate thickness",
		"has been released to the workshop with the revised cutting list",
		"is awaiting completion of the weld inspection",
		"will be packed for delivery after the final quality check",
		"requires the machined faces to be protected during transport",
		"has been returned to the fabrication bay for minor rework",
		"is ready for surface preparation and powder coating",
		"has been allocated a production slot on the CNC machine",
	}

	// Manufacturing: trailing sentences.
	mfgContexts = []string{
		"Please confirm the latest drawing revision before cutting the steel.",
		"Material certificates and heat numbers are recorded against the fabrication job.",
		"Check the weld size and joint preparation against the approved welding procedure.",
		"The workshop supervisor will confirm the fabrication sequence before assembly.",
		"Allow for the specified bend radius when preparing the sheet metal blanks.",
		"Finished dimensions must be checked before the assembly leaves the workshop.",
		"Keep the machined surfaces clean and protect them during handling.",
		"The delivery date will be confirmed once welding and finishing are complete.",
		"Please advise whether the fabricated steel components require galvanising.",
		"The cutting list includes an allowance for machining and final fit-up.",
	}

	// Manufacturing: email openers.
	mfgOpeners = []string{
		"Following our review of the fabrication drawings",
		"Thank you for your enquiry about the steel fabrication job",
		"With reference to the metalwork order",
		"Following the workshop planning meeting",
		"Ahead of the scheduled laser-cutting run",
		"After checking the material schedule and welding requirements",
		"Please find below an update on the fabrication work",
	}

	// Manufacturing: email closing sentences.
	mfgClosers = []string{
		"Please confirm the drawing revision so the workshop can proceed with fabrication.",
		"We will send the inspection results once welding and dimensional checks are complete.",
		"Thank you for the opportunity to quote on your metalwork requirements.",
		"Please let us know if the steel grade, finish or delivery requirements have changed.",
		"We look forward to completing the fabrication and arranging delivery.",
	}

	// Manufacturing: document title nouns.
	mfgTitles = []string{
		"Job Specification", "Fabrication Quote", "Cutting List", "Welding Plan",
		"Material Schedule", "Workshop Update", "Inspection Report", "Production Order",
		"Shop Drawing Review", "Assembly Instructions", "Delivery Note", "Quality Record",
	}

	// Manufacturing: document title qualifiers.
	mfgQualifiers = []string{
		"Steel Fabrication", "Stainless Steel", "Aluminium Metalwork", "Laser Cutting",
		"CNC Machining", "Sheet Metal", "Structural Steel", "Welded Assembly",
		"Plate Processing", "Workshop Production", "Metal Finishing", "Pipe Fabrication",
	}

	// Manufacturing: short labels for subjects and line items.
	mfgLabels = []string{
		"Steel Fabrication", "Laser Cutting", "Weld Inspection", "Plate Nesting",
		"CNC Machining", "Sheet Metal", "Press Brake", "Steel Brackets",
		"Welding Review", "Material Check", "Cutting List", "Shop Drawings",
		"Workshop Job", "Machine Setup", "Fabrication Quote", "Metal Assembly",
		"Powder Coating", "Surface Finish", "Stainless Steel", "Aluminium Panels",
		"Production Run", "Quality Check", "Dimensional Check", "Delivery Schedule",
		"Material Traceability", "Workshop Planning", "Metalwork Review", "Weld Repair",
		"Steel Frames", "Fabrication Order", "Fit-Up Check", "Final Inspection",
	}
)

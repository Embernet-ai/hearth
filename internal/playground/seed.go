package playground

// What a playground is made of. THE file to edit when Hearth changes: add a
// lesson step when a feature lands, reword one when a button moves. The test
// (playground_integration_test.go) builds a playground from this and fails
// if any of it no longer works.
//
// Keys ("ana", "web") tie the pieces together; dates are days from the day
// the playground is built, so "overdue" and "due this week" stay true.

type seedPerson struct {
	Key, Name string
	Dept      string // a space key: the department they work in and can see
	Manager   bool
}

type seedSpace struct {
	Key, Name, Parent string // Parent "" is a department
}

type seedComment struct {
	By, Body string
}

type seedTask struct {
	Space, Title, Status string
	Priority             int16
	Assignee             string // a person key, "" for nobody
	Due                  int    // days from today; 0 is no due date
	Body                 string
	Checklist            []string
	Comments             []seedComment
	Minutes              int // time logged on it, by the assignee, yesterday
	// Parent is the title of a task earlier in the list that this one sits
	// inside: a subtask, the way a course holds one task per person.
	Parent string
}

type seedDoc struct {
	Space, Kind, Title, Body string
}

// A lesson is a task in Tutorial whose checklist is the steps. Track is the
// sub-folder (Everyone, Managers, Admins); each step's Where says where the
// thing it asks for lives.
type seedLesson struct {
	Track, Title, Intro string
	Steps               []seedStep
}

type seedStep struct {
	Do, Where string
}

var seedSpaces = []seedSpace{
	{Key: "eng", Name: "Engineering"},
	{Key: "web", Name: "Website", Parent: "eng"},
	{Key: "field", Name: "Field Service", Parent: "eng"},
	{Key: "sales", Name: "Sales"},
	{Key: "ops", Name: "Operations"},
	{Key: "tutorial", Name: "Tutorial"},
	{Key: "t-everyone", Name: "Everyone", Parent: "tutorial"},
	{Key: "t-managers", Name: "Managers", Parent: "tutorial"},
	{Key: "t-admins", Name: "Admins", Parent: "tutorial"},
}

var seedPeople = []seedPerson{
	{Key: "ana", Name: "Armando Esteban Quito", Dept: "eng", Manager: true},
	{Key: "ben", Name: "Joe King", Dept: "eng"},
	{Key: "chloe", Name: "Marsha Mellow", Dept: "sales", Manager: true},
	{Key: "diego", Name: "Al O'Vera", Dept: "sales"},
	{Key: "emma", Name: "Elsa Pato", Dept: "ops"},
	{Key: "farid", Name: "Aquiles Bailo", Dept: "ops"},
}

var seedTasks = []seedTask{
	{Space: "web", Title: "Redesign the pricing page", Status: "in_progress", Priority: 2, Assignee: "ana", Due: 3,
		Body:      "The current page buries the **monthly** price. Lead with it, and keep the comparison table below.",
		Checklist: []string{"Draft the new layout", "Review with Sales", "Ship it"},
		Comments: []seedComment{
			{By: "chloe", Body: "Customers ask about the monthly price first, so yes, lead with it."},
			{By: "ana", Body: "Draft is up. I'll post screenshots tomorrow."},
		},
		Minutes: 90},
	{Space: "web", Title: "Fix the broken contact form", Status: "blocked", Priority: 3, Assignee: "ben", Due: -2,
		Body:     "Submissions fail on mobile. Waiting on the hosting provider to open the mail port.",
		Comments: []seedComment{{By: "ben", Body: "Ticket opened with the provider. Nothing to do until they reply."}},
		Minutes:  45},
	{Space: "web", Title: "Write the release notes for October", Status: "todo", Priority: 1, Assignee: "ben", Due: 6},
	{Space: "web", Title: "Add the new logo to the footer", Status: "done", Priority: 0, Assignee: "ana",
		Body: "Done and live."},
	{Space: "web", Title: "Old blog migration", Status: "cancelled", Priority: 0,
		Body: "Cancelled: we are keeping the old blog where it is."},
	{Space: "field", Title: "Quarterly inspection, North site", Status: "todo", Priority: 2, Assignee: "ben", Due: 10,
		Checklist: []string{"Check the pumps", "Photograph the meters", "Sign the report"}},
	{Space: "field", Title: "Replace the filter on unit 4", Status: "in_progress", Priority: 1, Assignee: "ana", Due: 1,
		Minutes: 30},
	{Space: "sales", Title: "Follow up with Northwind Traders", Status: "todo", Priority: 2, Assignee: "diego", Due: 2,
		Comments: []seedComment{{By: "chloe", Body: "They asked for a quote by Friday."}}},
	{Space: "sales", Title: "Prepare the Q4 sales deck", Status: "in_progress", Priority: 1, Assignee: "chloe", Due: 8,
		Minutes: 120},
	{Space: "sales", Title: "Update the price list", Status: "done", Priority: 1, Assignee: "diego"},
	{Space: "sales", Title: "Trade show booth (cancelled)", Status: "cancelled", Priority: 0, Assignee: "diego"},
	{Space: "ops", Title: "Order office supplies", Status: "todo", Priority: 0, Assignee: "emma", Due: 4},
	{Space: "ops", Title: "Renew the van insurance", Status: "todo", Priority: 3, Assignee: "farid", Due: -1,
		Body: "Overdue. The renewal letter is in the shared drive."},
	{Space: "ops", Title: "Plan the team lunch", Status: "in_progress", Priority: 0, Assignee: "farid", Due: 12,
		Comments: []seedComment{{By: "emma", Body: "Pizza again? 🍕"}, {By: "farid", Body: "Tacos this time. And music. 💃"}}},
	// A task used as a folder: the course, then one subtask per person taking it.
	{Space: "ops", Title: "Safety training", Status: "in_progress", Priority: 1,
		Body: "Everyone does it once a year. Each person has their own subtask below, so you can see who is done."},
	{Space: "ops", Title: "Joe King", Status: "done", Assignee: "ben", Parent: "Safety training"},
	{Space: "ops", Title: "Elsa Pato", Status: "in_progress", Assignee: "emma", Parent: "Safety training"},
	{Space: "ops", Title: "Aquiles Bailo", Status: "todo", Assignee: "farid", Parent: "Safety training"},
}

var seedDocs = []seedDoc{
	{Space: "tutorial", Kind: "doc", Title: "Welcome to your playground",
		Body: `# Welcome to Ember Demo Co

This is **your own** practice company. Nothing you do here touches real work, and nobody else sees it.

- Break things on purpose. That's what it's for.
- Start with the lessons in **Tutorial → Everyone**.
- Managers and admins: there are lessons for you too, further down.

When you want a clean slate: **Settings → Tutorial → Reset my playground**.`},
	{Space: "eng", Kind: "daily_report", Title: "Daily report — Engineering",
		Body: "**Done:** footer logo is live.\n\n**Doing:** pricing page draft, unit 4 filter.\n\n**Blocked:** contact form, waiting on the hosting provider."},
}

// A time-off request somebody is waiting on. In is days from today; it
// starts on a weekday so it always counts some working days.
type seedLeave struct {
	By, Kind, Reason string
	In, Length       int
}

var seedTimeOff = []seedLeave{
	{By: "ben", Kind: "vacation", Reason: "Cousin's wedding", In: 14, Length: 5},
}

var seedLessons = []seedLesson{
	{Track: "t-everyone", Title: "Lesson 1: The basics",
		Intro: "Tick each step as you go (or let Hearth tick it for you when it notices).",
		Steps: []seedStep{
			{Do: "Open a task with the 👁 button, without leaving the list", Where: "Tasks, the eye at the start of each row"},
			{Do: "Change that task's status", Where: "The Status box at the top of a task"},
			{Do: "Assign a task to Armando Esteban Quito", Where: "A task → the Assign button under its title"},
			{Do: "Tick a step on a task's checklist", Where: "Website → Redesign the pricing page → Checklist"},
			{Do: "Open Safety training and see who has finished it", Where: "Operations → Safety training → Subtasks"},
			{Do: "Add yourself as a subtask of Safety training", Where: "Safety training → Subtasks → Add a subtask"},
			{Do: "Unfold Safety training in the task list", Where: "Tasks → the ▸ next to Safety training"},
		}},
	{Track: "t-everyone", Title: "Lesson 2: Board, Calendar and Timeline",
		Steps: []seedStep{
			{Do: "Switch to Board and drag a Todo task into Done", Where: "Tasks → Board, at the top of the list"},
			{Do: "Click Show closed and drag a Cancelled task into In progress", Where: "Tasks → Board → Show closed"},
			{Do: "Find the overdue van insurance on the Calendar", Where: "Calendar in the sidebar, or Tasks → Calendar"},
			{Do: "Give a task a new due date", Where: "The Due box at the top of a task"},
			{Do: "Look at the Timeline view", Where: "Tasks → Timeline"},
		}},
	{Track: "t-everyone", Title: "Lesson 3: Filters and saved views",
		Steps: []seedStep{
			{Do: "Filter the task list to Joe King's tasks", Where: "Tasks → Filter"},
			{Do: "Group the list by Assignee", Where: "Tasks → Group By"},
			{Do: "Save it as a view called \"Joe's work\"", Where: "Tasks → Save view"},
			{Do: "Open your view, change one filter, and save it under a new name", Where: "Views in the sidebar"},
		}},
	{Track: "t-everyone", Title: "Lesson 4: Writing",
		Steps: []seedStep{
			{Do: "Post a comment with something in bold", Where: "Any task → Comments → the B button"},
			{Do: "Add a checklist to a comment", Where: "The ☑ Checklist button above the comment box"},
			{Do: "Put a YouTube video in a comment", Where: "The ▶ Video button: paste any YouTube link"},
			{Do: "Put a video in a task's Details", Where: "Any task → Edit → the ▶ Video button above Details"},
			{Do: "Attach a video file and play it", Where: "Any task → Attachments → choose an MP4, then Attach"},
			{Do: "Check how it looks before posting", Where: "The 👁 Preview button"},
			{Do: "Write in the big editor", Where: "The ⤢ Open big button"},
			{Do: "Edit a comment you posted", Where: "The Edit button under your comment"},
		}},
	{Track: "t-everyone", Title: "Lesson 5: Time and daily reports",
		Steps: []seedStep{
			{Do: "Log 30 minutes on a task", Where: "Any task → Time"},
			{Do: "See the hours you logged", Where: "My time in the sidebar"},
			{Do: "Write a daily report", Where: "All documents → Daily reports → New daily report"},
			{Do: "Request a day off", Where: "Time off in the sidebar"},
			{Do: "Check which departments you are in", Where: "Settings → My departments"},
		}},
	{Track: "t-managers", Title: "For managers: your team's work",
		Intro: "This part is for managers. If that's not you, feel free to skip it.",
		Steps: []seedStep{
			{Do: "See who is overloaded", Where: "Workload in the sidebar"},
			{Do: "Approve logged hours, one person at a time", Where: "Approvals → pick a department, then a person"},
			{Do: "See everyone's hours for the month", Where: "Time report in the sidebar"},
			{Do: "Approve Joe King's time off request", Where: "Time off in the sidebar → To decide"},
			{Do: "Read Engineering's daily report", Where: "Engineering → Daily reports"},
		}},
	{Track: "t-admins", Title: "For admins: people and access",
		Intro: "This part is for admins. If that's not you, feel free to skip it.",
		Steps: []seedStep{
			{Do: "Move Al O'Vera into Engineering", Where: "Settings → People → Al O'Vera → Works in"},
			{Do: "Let Aquiles Bailo see Sales' work", Where: "Settings → People → Aquiles Bailo → Can see work in"},
			{Do: "Make Elsa Pato a manager of Operations", Where: "Settings → People → Elsa Pato"},
			{Do: "Add a folder inside a department", Where: "Spaces & folders → the + on a department"},
			{Do: "Drag a department to reorder it", Where: "Spaces & folders → drag a row by the ⠿ handle"},
			{Do: "Try to remove a folder that still has work (Hearth won't let you)", Where: "Spaces & folders → Edit → Remove"},
		}},
}

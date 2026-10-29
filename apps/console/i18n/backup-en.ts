export const backupEn = {
  section: "Backup routes",
  sectionHelp:
    "Used only when no official route can serve a request: every official key is failing, rejected, rate-limited or out of budget. Customers keep the same model and price.",
  scopeNote:
    "Backups serve Chat Completions and Messages. Responses and token counting stay on official routes, because a different provider cannot continue their stored state.",
  noteTitle: "Coverage and spending",
  spendNote:
    "Spending limits live in your OpenRouter or ZenMux account. Usage is recorded at each model's upstream cost rate.",
  add: "Add backup",
  edit: "Edit backup",
  eyebrow: "Backup route",
  addIntro:
    "Connect an OpenRouter or ZenMux key. RelayPine sends a request here only after every official route for its model is unavailable.",
  editIntro: "Leave the API key empty to keep the saved key.",
  vendor: "Provider",
  vendorHelp: {
    openrouter: "Chat Completions and Messages through openrouter.ai.",
    zenmux: "Chat Completions and Messages through zenmux.ai.",
  },
  alreadyAdded: "Already added",
  apiKey: "API key",
  replaceKey: "Replace API key",
  keyHelp: "Stored encrypted and never shown again.",
  keepKey: "Leave empty to keep the saved key.",
  keyRequired: "Enter the API key.",
  check: "Check key",
  recheck: "Check again",
  keyAccepted: "Key accepted",
  keyLabel: "Key label",
  keyLimit: "Key credit left",
  coverage: "of {total} enabled models available",
  coverageHelp:
    "Matched by official model name. A model without a match always stays on official routes.",
  coverageEmpty:
    "No enabled models yet. Coverage is checked against your model catalog.",
  notOffered: "Not offered",
  noMatch: "No matching name",
  checkScope:
    "The check reads key details and the public model list. It does not run paid inference.",
  advanced: "Advanced settings",
  order: "Order",
  orderHelp: "When both backups are available, the higher number is tried first.",
  limitsHelp:
    "Local guardrails for this backup. During an outage it can receive all of the traffic that official routes cannot take.",
  invalidLimit: "Enter a whole number in the supported range.",
  saveUnchecked: "Save without checking",
  saveUncheckedHelp:
    "Use this only if the provider's key check is unavailable. The key is first used during an outage.",
  serves: "Serves",
  servesValue: "Chat · Messages",
  emptyTitle: "No backup routes",
  emptyBody:
    "Add OpenRouter or ZenMux to keep serving requests when official APIs are unavailable.",
  allAdded: "Both supported providers are added.",
  routingTitle: "Backup routes",
  routingBody:
    "OpenRouter and ZenMux take a request only after every official route has failed or is unavailable, in the order set under Upstreams.",
  requestBackup: "Backup",
  routesTried: "Routes tried",
  routeHelp:
    "Each refused route ran no inference and has no upstream cost. The customer is charged once, at the request's price.",
  requestAttempts: "Tried {count} upstreams",
  attemptRerouted: "Rerouted",
  savedToast: "Backup saved",
  balance: "Balance",
  refreshBalances: "Refresh balances",
  accountBalance: "Account balance",
  keyLimitLeft: "Key limit: {amount} left",
  balanceUpdated: "Updated {time}",
  balanceNeedsKey: "Add a management key to show the balance",
  managementKey: "Management key (optional)",
  managementKeyHelp: {
    openrouter:
      "Usually not needed: the API key above already reads the account balance. Add one only if no balance appears. OpenRouter management keys can also create and delete API keys.",
    zenmux:
      "Lets RelayPine read the pay-as-you-go balance. Create a Management API key in the ZenMux console; inference keys cannot read balances.",
  },
  managementKeySaved: "A management key is saved. Leave empty to keep it.",
  removeManagementKey: "Remove the saved management key",
} as const;

type Widen<T> = T extends string ? string : { [K in keyof T]: Widen<T[K]> };
export type BackupCopy = Widen<typeof backupEn>;

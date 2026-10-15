import { api, type QueryOf, type Schemas } from "@/api/client";

export type AddressBook = Schemas["AddressBook"];
export type Entry = Schemas["Entry"];
export type Tag = Schemas["Tag"];
export type Share = Schemas["AddressBookShare"];
export type Permission = Schemas["Permission"];
export type BookListQuery = QueryOf<"listAddressBooks">;
export type EntryListQuery = QueryOf<"listAddressBookEntries">;

const b = (id: number) => `/address-books/${id}`;

export const addressBooksApi = {
  list: (query: BookListQuery) => api.request<Schemas["AddressBookPage"]>("/address-books", { query }),
  get: (id: number) => api.request<AddressBook>(b(id)),
  create: (body: Schemas["AddressBookCreate"]) =>
    api.request<AddressBook>("/address-books", { method: "POST", body }),
  update: (id: number, body: Schemas["AddressBookUpdate"]) =>
    api.request<AddressBook>(b(id), { method: "PATCH", body }),
  remove: (id: number) => api.request<void>(b(id), { method: "DELETE" }),

  shares: (id: number) => api.request<{ items: Share[] }>(`${b(id)}/shares`),
  addShare: (id: number, body: Schemas["AddressBookShareCreate"]) =>
    api.request<Share>(`${b(id)}/shares`, { method: "POST", body }),
  updateShare: (id: number, shareId: number, permission: Permission) =>
    api.request<Share>(`${b(id)}/shares/${shareId}`, { method: "PATCH", body: { permission } }),
  removeShare: (id: number, shareId: number) =>
    api.request<void>(`${b(id)}/shares/${shareId}`, { method: "DELETE" }),

  entries: (id: number, query: EntryListQuery) =>
    api.request<Schemas["EntryPage"]>(`${b(id)}/entries`, { query }),
  createEntry: (id: number, body: Schemas["EntryCreate"]) =>
    api.request<Entry>(`${b(id)}/entries`, { method: "POST", body }),
  updateEntry: (id: number, entryId: number, body: Schemas["EntryUpdate"]) =>
    api.request<Entry>(`${b(id)}/entries/${entryId}`, { method: "PATCH", body }),
  removeEntry: (id: number, entryId: number) =>
    api.request<void>(`${b(id)}/entries/${entryId}`, { method: "DELETE" }),
  bulkEntries: (id: number, body: Schemas["EntryBulkRequest"]) =>
    api.request<Schemas["BulkResult"]>(`${b(id)}/entries/bulk`, { method: "POST", body }),
  addFromDevices: (id: number, body: Schemas["EntriesFromDevicesRequest"]) =>
    api.request<Schemas["BulkResult"]>(`${b(id)}/entries/from-devices`, { method: "POST", body }),

  tags: (id: number) => api.request<{ items: Tag[] }>(`${b(id)}/tags`),
  createTag: (id: number, body: Schemas["TagInput"]) =>
    api.request<Tag>(`${b(id)}/tags`, { method: "POST", body }),
  updateTag: (id: number, tagId: number, body: Schemas["TagPatch"]) =>
    api.request<Tag>(`${b(id)}/tags/${tagId}`, { method: "PATCH", body }),
  removeTag: (id: number, tagId: number) => api.request<void>(`${b(id)}/tags/${tagId}`, { method: "DELETE" }),
};

export const bookKeys = {
  all: ["address-books"] as const,
  list: (q: BookListQuery) => ["address-books", "list", q] as const,
  entries: (id: number, q?: EntryListQuery) => ["address-books", id, "entries", q] as const,
  tags: (id: number) => ["address-books", id, "tags"] as const,
  shares: (id: number) => ["address-books", id, "shares"] as const,
};

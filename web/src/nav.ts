import {
  Activity,
  BookUser,
  Contact,
  Download,
  FileClock,
  KeyRound,
  LayoutDashboard,
  Monitor,
  MonitorSmartphone,
  Server,
  Settings,
  Shield,
  ShieldCheck,
  UserCog,
  Users,
  type LucideIcon,
} from "lucide-react";

/** One sidebar entry. `labelKey` is an i18n key; `path` doubles as the route path. */
export interface NavItem {
  path: string;
  labelKey: string;
  icon: LucideIcon;
}

export interface NavSection {
  titleKey: string;
  /** Sections marked adminOnly are hidden for the `user` role (SPEC §4). */
  adminOnly: boolean;
  items: NavItem[];
}

/** Page list from SPEC §4: everyone sees Overview + My; admins also see System. */
export const NAV: NavSection[] = [
  {
    titleKey: "nav.overview",
    adminOnly: false,
    items: [{ path: "/", labelKey: "nav.dashboard", icon: LayoutDashboard }],
  },
  {
    titleKey: "nav.my",
    adminOnly: false,
    items: [
      { path: "/my/profile", labelKey: "nav.myProfile", icon: ShieldCheck },
      { path: "/my/devices", labelKey: "nav.myDevices", icon: MonitorSmartphone },
      { path: "/my/address-books", labelKey: "nav.myAddressBooks", icon: Contact },
      { path: "/my/sessions", labelKey: "nav.mySessions", icon: Activity },
      { path: "/my/install", labelKey: "nav.installClient", icon: Download },
    ],
  },
  {
    titleKey: "nav.system",
    adminOnly: true,
    items: [
      { path: "/devices", labelKey: "nav.devices", icon: Monitor },
      { path: "/users", labelKey: "nav.users", icon: Users },
      { path: "/groups", labelKey: "nav.groups", icon: UserCog },
      { path: "/acl", labelKey: "nav.acl", icon: Shield },
      { path: "/address-books", labelKey: "nav.addressBooks", icon: BookUser },
      { path: "/audit", labelKey: "nav.audit", icon: FileClock },
      { path: "/identity-providers", labelKey: "nav.identityProviders", icon: KeyRound },
      { path: "/server", labelKey: "nav.server", icon: Server },
      { path: "/settings", labelKey: "nav.settings", icon: Settings },
    ],
  },
];

/** Sections visible for a role. */
export function visibleSections(isAdmin: boolean): NavSection[] {
  return NAV.filter((s) => isAdmin || !s.adminOnly);
}

/** All nav items, flattened (used to generate placeholder routes). */
export const NAV_ITEMS: NavItem[] = NAV.flatMap((s) => s.items);

import React, { useMemo, useState } from 'react';
import { qs, request, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatRelative, useTranslation } from '@oneclub/i18n';
import { useAuth } from '../context';
import {
  Card, Checkbox, ConfirmDialog, DataTable, Drawer, ErrorAlert, FilterPills, Icon, PageHeader, PasswordField, SearchBox, SelectField, StatusPill,
  TextField, fieldErrors, useDebounced,
} from '../components/ui';
import { useToast } from '../components/toast';

type User = Schemas['User'];
type Role = Schemas['Role'];
type Assignment = Schemas['Assignment'];
type Perm = Schemas['PermissionInfo'];

function useRoles() {
  return useGet<Page<Role>>('/api/v1/platform/roles');
}

function useAllProperties() {
  const { me } = useAuth();
  return me?.properties ?? [];
}

function AssignmentEditor({ value, onChange }: { value: { roleId: string; propertyId: string }[]; onChange: (v: { roleId: string; propertyId: string }[]) => void }) {
  const roles = useRoles();
  const props = useAllProperties();
  const byId = new Map((roles.data?.items ?? []).map((r) => [r.id, r]));
  return (
    <div className="oc-stack">
      {value.map((a, i) => {
        const scope = byId.get(a.roleId)?.scope;
        return (
          <div key={i} className="oc-row-wrap">
            <div style={{ flex: 2, minWidth: 220 }}>
              <SelectField label="Role" value={a.roleId} placeholder="Select…" onChange={(v) => onChange(value.map((x, j) => (j === i ? { ...x, roleId: v } : x)))}
                options={(roles.data?.items ?? []).filter((r) => r.status === 'active').map((r) => ({ value: r.id, label: `${r.name} · ${r.category}` }))} />
            </div>
            <div style={{ flex: 1, minWidth: 180 }}>
              {scope === 'property' || !scope ? (
                <SelectField label="Property" value={a.propertyId} placeholder="Select…" onChange={(v) => onChange(value.map((x, j) => (j === i ? { ...x, propertyId: v } : x)))}
                  options={props.map((p) => ({ value: p.id, label: p.name }))} />
              ) : <div className="oc-field"><span className="oc-label">Property</span><span className="oc-muted" style={{ height: 44, display: 'flex', alignItems: 'center' }}>All properties</span></div>}
            </div>
            <button type="button" className="oc-icon-btn" style={{ alignSelf: 'flex-end', marginBottom: 2 }} aria-label="Remove role" onClick={() => onChange(value.filter((_, j) => j !== i))}>
              <Icon name="close" size={18} />
            </button>
          </div>
        );
      })}
      <div><button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onChange([...value, { roleId: '', propertyId: props[0]?.id ?? '' }])}>
        <Icon name="add" size={18} /> Add role</button></div>
    </div>
  );
}

function CreateUser({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const roles = useRoles();
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [phone, setPhone] = useState('');
  const [locale, setLocale] = useState('id');
  const [pw, setPw] = useState('');
  const [assign, setAssign] = useState<{ roleId: string; propertyId: string }[]>([{ roleId: '', propertyId: useAllProperties()[0]?.id ?? '' }]);
  const save = useSend('POST', '/api/v1/platform/users', ['/api/v1/platform/users']);
  const fe = fieldErrors(save.error);
  const scopeOf = (id: string) => roles.data?.items.find((r) => r.id === id)?.scope;
  return (
    <form className="oc-stack" onSubmit={(e) => {
      e.preventDefault();
      save.mutate({
        email, fullName: name, ...(phone ? { phone } : {}), locale, ...(pw ? { password: pw } : {}),
        assignments: assign.filter((a) => a.roleId).map((a) => ({ roleId: a.roleId, propertyId: scopeOf(a.roleId) === 'property' ? a.propertyId : null })),
      }, { onSuccess: () => { toast(t('common.saved')); onDone(); } });
    }}>
      <p className="oc-muted" style={{ margin: 0 }}>{t('help.users')}</p>
      <div className="oc-form">
        <TextField label="E-mail" type="email" value={email} onChange={setEmail} required error={fe.email} />
        <TextField label="Full Name" value={name} onChange={setName} required error={fe.fullName} />
        <TextField label="Phone" value={phone} onChange={setPhone} />
        <SelectField label="Language" value={locale} onChange={setLocale} options={[{ value: 'id', label: 'Bahasa Indonesia' }, { value: 'en', label: 'English' }]} />
        <PasswordField label="Initial password (optional)" value={pw} onChange={setPw} span help="Without a password the user receives an invitation e-mail." error={fe.password} />
      </div>
      <div className="oc-label">Roles</div>
      <AssignmentEditor value={assign} onChange={setAssign} />
      {fe.assignments && <div className="oc-field-error">{fe.assignments}</div>}
      <ErrorAlert error={Object.keys(fe).length ? null : save.error} />
      <div className="oc-row"><span className="oc-spacer" /><button type="button" className="oc-btn oc-btn-neutral" onClick={onDone}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={save.isPending}>Add User</button></div>
    </form>
  );
}

function UserDetail({ user, onClose }: { user: User; onClose: () => void }) {
  const { can } = useAuth();
  const { t } = useTranslation();
  const toast = useToast();
  const props = useAllProperties();
  const roles = useRoles();
  const [name, setName] = useState(user.fullName);
  const [phone, setPhone] = useState(user.phone ?? '');
  const [newAssign, setNewAssign] = useState<{ roleId: string; propertyId: string }[]>([]);
  const [confirm, setConfirm] = useState<'deactivate' | 'activate' | 'reset-mfa' | null>(null);
  const [pin, setPin] = useState('');
  const detail = useGet<User>(`/api/v1/platform/users/${user.id}`);
  const u = detail.data ?? user;
  const save = useSend('PATCH', `/api/v1/platform/users/${user.id}`, ['/api/v1/platform/users']);
  const act = useSend<{ reason: string }>('POST', () => `/api/v1/platform/users/${user.id}:${confirm}`, ['/api/v1/platform/users']);
  const pinM = useSend('PUT', `/api/v1/platform/users/${user.id}/pin`);
  const addA = useSend<{ userId: string; roleId: string; propertyId: string | null }>('POST', '/api/v1/platform/role-assignments', ['/api/v1/platform/users']);
  const scopeOf = (id: string) => roles.data?.items.find((r) => r.id === id)?.scope;
  const removeA = async (a: Schemas['UserAssignment']) => {
    await request('DELETE', `/api/v1/platform/role-assignments/${a.id}`);
    await detail.refetch();
    toast(t('common.saved'));
  };
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <StatusPill status={u.status} />
        {u.locked && <StatusPill status="suspended" label="Locked" />}
        <span className="oc-small oc-muted">MFA {u.mfaEnabled ? 'on' : 'off'} · PIN {u.hasPin ? 'set' : 'not set'} · last login {u.lastLoginAt ? formatRelative(u.lastLoginAt) : 'never'}</span>
      </div>
      {can('platform.user.update') && (
        <form className="oc-form" onSubmit={(e) => { e.preventDefault(); save.mutate({ fullName: name, phone }, { onSuccess: () => toast(t('common.saved')) }); }}>
          <TextField label="Full Name" value={name} onChange={setName} />
          <TextField label="Phone" value={phone} onChange={setPhone} />
          <div className="oc-span"><button className="oc-btn oc-btn-ink oc-btn-sm" disabled={save.isPending}>Save</button></div>
        </form>
      )}
      <div className="oc-label">Roles</div>
      <DataTable rows={u.assignments as unknown as Record<string, unknown>[]} rowKey={(a) => String(a.id)}
        columns={[{ key: 'roleName', header: 'Role' }, { key: 'propertyName', header: 'Property', render: (a) => (a.propertyName as string) ?? 'All properties' }]}
        actions={(a) => can('platform.role_assignment.manage') && (
          <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => void removeA(a as unknown as Schemas['UserAssignment'])}>Remove</button>
        )} />
      {can('platform.role_assignment.manage') && (
        <>
          <AssignmentEditor value={newAssign} onChange={setNewAssign} />
          {newAssign.length > 0 && <div><button className="oc-btn oc-btn-ink oc-btn-sm" onClick={async () => {
            for (const a of newAssign.filter((x) => x.roleId)) {
              await addA.mutateAsync({ userId: user.id, roleId: a.roleId, propertyId: scopeOf(a.roleId) === 'property' ? a.propertyId : null });
            }
            setNewAssign([]);
            await detail.refetch();
            toast(t('common.saved'));
          }}>Save roles</button></div>}
          <ErrorAlert error={addA.error} />
        </>
      )}
      {can('platform.user.update') && (
        <form className="oc-row-wrap" onSubmit={(e) => { e.preventDefault(); pinM.mutate({ pin } as never, { onSuccess: () => { setPin(''); toast(t('common.saved')); void detail.refetch(); } }); }}>
          <div style={{ width: 200 }}><TextField label="Staff PIN" inputMode="numeric" maxLength={6} value={pin} onChange={setPin} error={fieldErrors(pinM.error).pin} /></div>
          <button className="oc-btn oc-btn-neutral" style={{ alignSelf: 'flex-end' }} disabled={pin.length !== 6}>Set PIN</button>
        </form>
      )}
      <div className="oc-row-wrap">
        {can('platform.user.deactivate') && (u.status === 'active'
          ? <button className="oc-btn oc-btn-danger" onClick={() => setConfirm('deactivate')}>Deactivate</button>
          : <button className="oc-btn oc-btn-primary" onClick={() => setConfirm('activate')}>Activate</button>)}
        {can('platform.user.reset_mfa') && u.mfaEnabled && <button className="oc-btn oc-btn-outline" onClick={() => setConfirm('reset-mfa')}>Reset MFA</button>}
        <span className="oc-spacer" />
        <span className="oc-small oc-muted">Created {formatDateTime(u.createdAt)} · {props.length} propert{props.length === 1 ? 'y' : 'ies'} in scope</span>
      </div>
      <ConfirmDialog open={!!confirm} onClose={() => { setConfirm(null); act.reset(); }} busy={act.isPending} error={act.error}
        title={confirm === 'deactivate' ? 'Deactivate user' : confirm === 'activate' ? 'Activate user' : 'Reset MFA'}
        message={confirm === 'deactivate' ? 'All sessions are revoked immediately.' : confirm === 'reset-mfa' ? 'The user sets up a new authenticator at next login.' : undefined}
        confirmLabel={confirm === 'deactivate' ? 'Deactivate' : confirm === 'activate' ? 'Activate' : 'Reset MFA'} danger={confirm !== 'activate'}
        reason={confirm === 'reset-mfa' ? 'required' : 'optional'}
        onConfirm={(reason) => act.mutate({ reason }, { onSuccess: () => { setConfirm(null); toast(t('common.saved')); void detail.refetch(); if (confirm !== 'reset-mfa') onClose(); } })} />
    </div>
  );
}

export function UsersPage() {
  const { can } = useAuth();
  const { t } = useTranslation();
  const [q, setQ] = useState('');
  const [status, setStatus] = useState('active');
  const [creating, setCreating] = useState(false);
  const [open, setOpen] = useState<User | null>(null);
  const query = useDebounced(q);
  const list = useGet<Page<User>>(`/api/v1/platform/users${qs({ q: query, 'filter[status]': status, limit: 200 })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Users" help={t('help.users')} actions={can('platform.user.create') &&
        <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}><Icon name="person_add" size={18} /> Add User</button>} />
      <div className="oc-card">
        <div className="oc-toolbar">
          <SearchBox value={q} onChange={setQ} />
          <span className="oc-spacer" />
          <FilterPills options={[{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }, { value: '', label: 'All' }]} value={status} onChange={setStatus} />
        </div>
        <DataTable rows={list.data?.items as unknown as Record<string, unknown>[]} loading={list.isLoading} error={list.error}
          onRowClick={(r) => setOpen(r as unknown as User)}
          columns={[
            { key: 'fullName', header: 'Name', render: (r) => <div><div style={{ fontWeight: 600 }}>{String(r.fullName)}</div><div className="oc-small oc-muted">{String(r.email)}</div></div> },
            { key: 'roles', header: 'Roles', render: (r) => (r.assignments as Schemas['UserAssignment'][]).map((a) => a.roleName).filter((v, i, a) => a.indexOf(v) === i).join(', ') },
            { key: 'mfa', header: 'MFA', render: (r) => (r.mfaEnabled ? <Icon name="verified_user" size={18} label="MFA enabled" /> : <span className="oc-muted">—</span>) },
            { key: 'lastLoginAt', header: 'Last Login', render: (r) => (r.lastLoginAt ? formatRelative(String(r.lastLoginAt)) : <span className="oc-muted">Never</span>) },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
          ]} />
      </div>
      <Drawer open={creating} onClose={() => setCreating(false)} title="Add User">{creating && <CreateUser onDone={() => setCreating(false)} />}</Drawer>
      <Drawer open={!!open} onClose={() => setOpen(null)} title={open?.fullName ?? ''}>{open && <UserDetail key={open.id} user={open} onClose={() => setOpen(null)} />}</Drawer>
    </div>
  );
}

function PermissionMatrix({ perms, selected, onToggle, readOnly }: { perms: Perm[]; selected: Set<string>; onToggle: (code: string) => void; readOnly?: boolean }) {
  const groups = useMemo(() => {
    const g = new Map<string, Perm[]>();
    for (const p of perms) g.set(p.module, [...(g.get(p.module) ?? []), p]);
    return [...g.entries()];
  }, [perms]);
  return (
    <div className="oc-stack">
      {groups.map(([mod, ps]) => (
        <div key={mod}>
          <div className="oc-label" style={{ textTransform: 'capitalize', marginBottom: 6 }}>{mod}</div>
          <div className="oc-row-wrap">
            {ps.map((p) => (
              <button key={p.code} type="button" className="oc-chip" aria-pressed={selected.has(p.code)} disabled={readOnly} title={p.description}
                onClick={() => onToggle(p.code)} style={{ opacity: readOnly && !selected.has(p.code) ? 0.5 : 1 }}>
                {p.object.replace(/_/g, ' ')} · {p.action.replace(/_/g, ' ')}
              </button>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function RoleEditor({ role, onDone }: { role?: Role; onDone: () => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const perms = useGet<Page<Perm>>('/api/v1/platform/permissions');
  const [code, setCode] = useState(role?.code ?? '');
  const [name, setName] = useState(role?.name ?? '');
  const [category, setCategory] = useState(role?.category ?? 'Custom');
  const [scope, setScope] = useState(role?.scope ?? 'property');
  const [mfa, setMfa] = useState(role?.mfaRequired ?? false);
  const [sel, setSel] = useState(new Set(role?.permissions ?? []));
  const readOnly = !!role?.isTemplate;
  const save = useSend(role ? 'PATCH' : 'POST', role ? `/api/v1/platform/roles/${role.id}` : '/api/v1/platform/roles', ['/api/v1/platform/roles']);
  const fe = fieldErrors(save.error);
  return (
    <form className="oc-stack" onSubmit={(e) => {
      e.preventDefault();
      const body = role ? { name, category, mfaRequired: mfa, permissions: [...sel] } : { code, name, category, scope, mfaRequired: mfa, permissions: [...sel] };
      save.mutate(body, { onSuccess: () => { toast(t('common.saved')); onDone(); } });
    }}>
      {readOnly && <div className="oc-alert oc-alert-info">{t('help.roles')}</div>}
      <div className="oc-form">
        {!role && <TextField label="Code" value={code} onChange={setCode} required help="lower_case_with_underscores" error={fe.code} />}
        <TextField label="Name" value={name} onChange={setName} required disabled={readOnly} error={fe.name} />
        <TextField label="Category" value={category} onChange={setCategory} disabled={readOnly} />
        {!role && <SelectField label="Scope" value={scope} onChange={(v) => setScope(v as Role['scope'])} options={[{ value: 'property', label: 'Per property' }, { value: 'instance', label: 'All properties' }]} />}
        <div className="oc-field"><span className="oc-label">&nbsp;</span><Checkbox label="Requires MFA" checked={mfa} disabled={readOnly} onChange={setMfa} /></div>
      </div>
      <div className="oc-label">Permissions ({sel.size})</div>
      {fe.permissions && <div className="oc-field-error">{fe.permissions}</div>}
      {perms.data && <PermissionMatrix perms={perms.data.items} selected={sel} readOnly={readOnly}
        onToggle={(c) => setSel((s) => { const n = new Set(s); if (n.has(c)) n.delete(c); else n.add(c); return n; })} />}
      <ErrorAlert error={Object.keys(fe).length ? null : save.error} />
      {!readOnly && <div className="oc-row"><span className="oc-spacer" /><button type="button" className="oc-btn oc-btn-neutral" onClick={onDone}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={save.isPending}>{role ? 'Save' : 'Add Role'}</button></div>}
    </form>
  );
}

export function RolesPage({ title = 'Roles & Permissions' }: { title?: string }) {
  const { can } = useAuth();
  const { t } = useTranslation();
  const roles = useRoles();
  const [cat, setCat] = useState('');
  const [edit, setEdit] = useState<Role | 'new' | null>(null);
  const [del, setDel] = useState<Role | null>(null);
  const delM = useSend('DELETE', () => `/api/v1/platform/roles/${del?.id}`, ['/api/v1/platform/roles']);
  const cats = [...new Set((roles.data?.items ?? []).map((r) => r.category))];
  const rows = (roles.data?.items ?? []).filter((r) => !cat || r.category === cat);
  return (
    <div className="oc-stack">
      <PageHeader title={title} help={t('help.roles')} actions={can('platform.role.create') &&
        <button className="oc-btn oc-btn-primary" onClick={() => setEdit('new')}><Icon name="add" size={18} /> Add Role</button>} />
      <div className="oc-card">
        <div className="oc-toolbar"><FilterPills options={[{ value: '', label: 'All' }, ...cats.map((c) => ({ value: c, label: c }))]} value={cat} onChange={setCat} /></div>
        <DataTable rows={rows as unknown as Record<string, unknown>[]} loading={roles.isLoading} error={roles.error} onRowClick={(r) => setEdit(r as unknown as Role)}
          columns={[
            { key: 'name', header: 'Role', render: (r) => <div><div style={{ fontWeight: 600 }}>{String(r.name)}</div><div className="oc-small oc-muted">{String(r.code)}</div></div> },
            { key: 'category', header: 'Category' },
            { key: 'scope', header: 'Scope', render: (r) => (r.scope === 'property' ? 'Per property' : r.scope === 'instance' ? 'All properties' : 'Platform') },
            { key: 'permissions', header: 'Permissions', align: 'right', render: (r) => (r.permissions as string[]).length },
            { key: 'assignedUsers', header: 'Users', align: 'right' },
            { key: 'mfaRequired', header: 'MFA', render: (r) => (r.mfaRequired ? 'Required' : '—') },
            { key: 'isTemplate', header: 'Type', render: (r) => (r.isTemplate ? <StatusPill status="info" label="Template" /> : <StatusPill status="active" label="Custom" />) },
          ]}
          actions={(r) => !r.isTemplate && can('platform.role.delete') && <button className="oc-icon-btn" aria-label="Delete role" onClick={() => setDel(r as unknown as Role)}><Icon name="delete" size={18} /></button>} />
      </div>
      <Drawer open={!!edit} onClose={() => setEdit(null)} title={edit === 'new' ? 'Add Role' : (edit?.name ?? '')}>
        {edit && <RoleEditor key={edit === 'new' ? 'new' : edit.id} role={edit === 'new' ? undefined : edit} onDone={() => setEdit(null)} />}
      </Drawer>
      <ConfirmDialog open={!!del} onClose={() => setDel(null)} danger confirmLabel="Delete" title="Delete role" error={delM.error}
        message={`Delete ${del?.name}? Roles that are assigned must be unassigned first.`} onConfirm={() => delM.mutate(undefined, { onSuccess: () => setDel(null) })} />
    </div>
  );
}

export function PermissionsPage() {
  const perms = useGet<Page<Perm>>('/api/v1/platform/permissions');
  return (
    <div className="oc-stack">
      <PageHeader title="Permissions" help="Granular permissions <module>.<object>.<action>. Roles are made of permissions." />
      <Card>
        <DataTable rows={perms.data?.items as unknown as Record<string, unknown>[]} loading={perms.isLoading} rowKey={(r) => String(r.code)}
          columns={[{ key: 'code', header: 'Code', render: (r) => <code className="oc-code">{String(r.code)}</code> }, { key: 'module', header: 'Module' },
            { key: 'description', header: 'Description' }, { key: 'platformOnly', header: 'Platform Admin only', render: (r) => (r.platformOnly ? 'Yes' : '—') }]} />
      </Card>
    </div>
  );
}

export type { Assignment };

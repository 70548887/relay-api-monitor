import type { QueryClient } from '@tanstack/react-query';
import { onSessionCleared } from './api';
import { onUserSessionCleared } from './userApi';

export function bindSessionCache(client:QueryClient){const unbindAdmin=onSessionCleared(()=>{
 const predicate=(query:{queryKey:readonly unknown[]})=>typeof query.queryKey[0]==='string'&&(query.queryKey[0]==='session'||query.queryKey[0].startsWith('admin-'));
 void client.cancelQueries({predicate});
 client.removeQueries({predicate});
});const unbindUser=onUserSessionCleared(()=>{
 const predicate=(query:{queryKey:readonly unknown[]})=>typeof query.queryKey[0]==='string'&&(query.queryKey[0]==='user-session'||query.queryKey[0].startsWith('user-'));
 void client.cancelQueries({predicate});client.removeQueries({predicate});
});return()=>{unbindAdmin();unbindUser()}}

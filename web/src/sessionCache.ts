import type { QueryClient } from '@tanstack/react-query';
import { onSessionCleared } from './api';

export function bindSessionCache(client:QueryClient){return onSessionCleared(()=>{
 const predicate=(query:{queryKey:readonly unknown[]})=>typeof query.queryKey[0]==='string'&&(query.queryKey[0]==='session'||query.queryKey[0].startsWith('admin-'));
 void client.cancelQueries({predicate});
 client.removeQueries({predicate});
})}

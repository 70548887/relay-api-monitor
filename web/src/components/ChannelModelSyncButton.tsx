import { App, Button } from 'antd';
import { SyncOutlined } from '@ant-design/icons';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { post } from '../api';
import type { Channel, Model } from '../types';

interface SyncResult { count:number; models:Model[] }

export function ChannelModelSyncButton({channel,disabled=false}:{channel:Channel;disabled?:boolean}) {
 const client=useQueryClient();
 const {message}=App.useApp();
 const sync=useMutation({
  mutationFn:()=>post<SyncResult>(`/admin/channels/${channel.id}/sync-models`,{}),
  onSuccess:async(result)=>{
   client.setQueryData<Model[]>(['admin-models'],current=>[
    ...(current||[]).filter(model=>model.channel_id!==channel.id),
    ...result.models,
   ]);
   client.setQueryData<Channel[]>(['admin-channels'],current=>current?.map(item=>item.id===channel.id?{...item,last_sync_at:new Date().toISOString(),sync_error:''}:item));
   await Promise.all([
    client.invalidateQueries({queryKey:['admin-intelligence-status']}),
   ]);
   void message.success(`已同步当前分组的 ${result.count} 个模型`);
  },
  onError:error=>void message.error((error as Error).message),
 });
 return <Button size="small" icon={<SyncOutlined aria-hidden="true" spin={sync.isPending}/>} loading={sync.isPending} disabled={disabled} onClick={()=>sync.mutate()}>同步模型</Button>;
}
